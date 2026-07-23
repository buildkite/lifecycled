package lifecycled

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

const (
	longPollingWaitTimeSeconds = 20
	queuePolicy                = `
{
  "Version":"2012-10-17",
  "Statement":[
    {
      "Effect":"Allow",
      "Principal":"*",
      "Action":"sqs:SendMessage",
      "Resource":"*",
      "Condition":{
        "ArnEquals":{
          "aws:SourceArn":"%s"
        }
      }
    }
  ]
}
`
)

// SQSClient is the subset of the SQS API used by the daemon.
//
//go:generate go tool mockgen -destination=mocks/mock_sqs_client.go -package=mocks github.com/buildkite/lifecycled SQSClient
type SQSClient interface {
	CreateQueue(context.Context, *sqs.CreateQueueInput, ...func(*sqs.Options)) (*sqs.CreateQueueOutput, error)
	GetQueueAttributes(context.Context, *sqs.GetQueueAttributesInput, ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error)
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	DeleteQueue(context.Context, *sqs.DeleteQueueInput, ...func(*sqs.Options)) (*sqs.DeleteQueueOutput, error)
}

// SNSClient is the subset of the SNS API used by the daemon.
//
//go:generate go tool mockgen -destination=mocks/mock_sns_client.go -package=mocks github.com/buildkite/lifecycled SNSClient
type SNSClient interface {
	Subscribe(context.Context, *sns.SubscribeInput, ...func(*sns.Options)) (*sns.SubscribeOutput, error)
	Unsubscribe(context.Context, *sns.UnsubscribeInput, ...func(*sns.Options)) (*sns.UnsubscribeOutput, error)
}

// Queue manages the SQS queue and SNS subscription.
type Queue struct {
	name            string
	url             string
	arn             string
	topicArn        string
	subscriptionArn string
	tags            string

	sqsClient SQSClient
	snsClient SNSClient
}

// NewQueue returns a new... Queue.
func NewQueue(queueName, topicArn string, sqsClient SQSClient, snsClient SNSClient, tags string) *Queue {
	return &Queue{
		name:      queueName,
		topicArn:  topicArn,
		sqsClient: sqsClient,
		snsClient: snsClient,
		tags:      tags,
	}
}

// Create the SQS queue.
func (q *Queue) Create(ctx context.Context) error {
	tags, err := parseTags(q.tags)
	if err != nil {
		return err
	}
	out, err := q.sqsClient.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String(q.name),
		Attributes: map[string]string{
			"Policy":                        fmt.Sprintf(queuePolicy, q.topicArn),
			"ReceiveMessageWaitTimeSeconds": strconv.Itoa(longPollingWaitTimeSeconds),
		},
		Tags: tags,
	})
	if err != nil {
		return err
	}
	q.url = aws.ToString(out.QueueUrl)
	return nil
}

// GetArn for the SQS queue.
func (q *Queue) getArn(ctx context.Context) (string, error) {
	if q.arn == "" {
		out, err := q.sqsClient.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
			AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
			QueueUrl:       aws.String(q.url),
		})
		if err != nil {
			return "", err
		}
		arn, ok := out.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]
		if !ok {
			return "", errors.New("no attribute QueueArn")
		}
		q.arn = arn
	}
	return q.arn, nil
}

// Subscribe the queue to an SNS topic
func (q *Queue) Subscribe(ctx context.Context) error {
	arn, err := q.getArn(ctx)
	if err != nil {
		return err
	}
	out, err := q.snsClient.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: aws.String(q.topicArn),
		Protocol: aws.String("sqs"),
		Endpoint: aws.String(arn),
	})
	if err != nil {
		return err
	}
	q.subscriptionArn = aws.ToString(out.SubscriptionArn)
	return nil
}

// GetMessages long polls for messages from the SQS queue.
func (q *Queue) GetMessages(ctx context.Context) ([]sqstypes.Message, error) {
	out, err := q.sqsClient.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(q.url),
		// SQS max per receive. Under SNS fan-out the queue also carries other
		// instances' events, so draining a batch per poll beats one-at-a-time.
		MaxNumberOfMessages: 10,
		WaitTimeSeconds:     longPollingWaitTimeSeconds,
		VisibilityTimeout:   0,
	})
	if err != nil {
		// Ignore error if the context was cancelled (i.e. we are shutting down)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, nil
		}
		return nil, err
	}
	return out.Messages, nil
}

// DeleteMessage from the queue.
func (q *Queue) DeleteMessage(ctx context.Context, receiptHandle string) error {
	_, err := q.sqsClient.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(q.url),
		ReceiptHandle: aws.String(receiptHandle),
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return err
	}
	return nil
}

// Unsubscribe the queue from the SNS topic.
func (q *Queue) Unsubscribe(ctx context.Context) error {
	_, err := q.snsClient.Unsubscribe(ctx, &sns.UnsubscribeInput{
		SubscriptionArn: aws.String(q.subscriptionArn),
	})
	return err
}

// Delete the SQS queue.
func (q *Queue) Delete(ctx context.Context) error {
	_, err := q.sqsClient.DeleteQueue(ctx, &sqs.DeleteQueueInput{
		QueueUrl: aws.String(q.url),
	})
	if err != nil {
		// Ignore error if queue does not exist (which is what we want)
		var notExist *sqstypes.QueueDoesNotExist
		if !errors.As(err, &notExist) {
			return err
		}
	}
	return nil
}

// Expects format like "key1=alpha,key2=beta"
func parseTags(input string) (map[string]string, error) {
	if input == "" {
		return nil, nil
	}

	const (
		maxTags        = 50
		maxKeyLength   = 128
		maxValueLength = 256
	)

	tags := make(map[string]string)
	pairs := strings.Split(input, ",")

	for _, pair := range pairs {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		if key == "" {
			continue
		}

		// Check key length
		if len(key) > maxKeyLength {
			return nil, fmt.Errorf("tag key exceeds maximum length of %d characters: %q", maxKeyLength, key)
		}

		// Check value length
		if len(value) > maxValueLength {
			return nil, fmt.Errorf("tag value exceeds maximum length of %d characters for key %q", maxValueLength, key)
		}

		// Check for aws: prefix (case-insensitive)
		if len(key) >= 4 && strings.ToLower(key[:4]) == "aws:" {
			return nil, fmt.Errorf("tag keys cannot start with 'aws:' prefix: %q", key)
		}

		tags[key] = value
	}

	// Check total number of tags
	if len(tags) > maxTags {
		return nil, fmt.Errorf("number of tags (%d) exceeds maximum allowed (%d)", len(tags), maxTags)
	}

	if len(tags) == 0 {
		return map[string]string{}, nil
	}

	return tags, nil
}
