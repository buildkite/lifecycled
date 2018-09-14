package lifecycled

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/sns"
	"github.com/aws/aws-sdk-go/service/sns/snsiface"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/aws/aws-sdk-go/service/sqs/sqsiface"
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

// SQSClient for testing purposes
//go:generate mockgen -destination=mocks/mock_sqs_client.go -package=mocks github.com/itsdalmo/lifecycled SQSClient
type SQSClient sqsiface.SQSAPI

// SNSClient for testing purposes
//go:generate mockgen -destination=mocks/mock_sns_client.go -package=mocks github.com/itsdalmo/lifecycled SNSClient
type SNSClient snsiface.SNSAPI

// Queue manages the SQS queue and SNS subscription.
type Queue struct {
	name            string
	url             string
	arn             string
	topicArn        string
	subscriptionArn string

	sqsClient SQSClient
	snsClient SNSClient
}

// NewQueue returns a new... Queue.
func NewQueue(queueName, topicArn string, sqsClient SQSClient, snsClient SNSClient) *Queue {
	return &Queue{
		name:      queueName,
		topicArn:  topicArn,
		sqsClient: sqsClient,
		snsClient: snsClient,
	}
}

// Create the SQS queue.
func (q *Queue) Create() error {
	out, err := q.sqsClient.CreateQueue(&sqs.CreateQueueInput{
		QueueName: aws.String(q.name),
		Attributes: map[string]*string{
			"Policy":                        aws.String(fmt.Sprintf(queuePolicy, q.topicArn)),
			"ReceiveMessageWaitTimeSeconds": aws.String(strconv.Itoa(longPollingWaitTimeSeconds)),
		},
	})
	if err != nil {
		return err
	}
	q.url = aws.StringValue(out.QueueUrl)
	return nil
}

// GetArn for the SQS queue.
func (q *Queue) getArn() (string, error) {
	if q.arn == "" {
		out, err := q.sqsClient.GetQueueAttributes(&sqs.GetQueueAttributesInput{
			AttributeNames: aws.StringSlice([]string{"QueueArn"}),
			QueueUrl:       aws.String(q.url),
		})
		if err != nil {
			return "", err
		}
		arn, ok := out.Attributes["QueueArn"]
		if !ok {
			return "", errors.New("No attribute QueueArn")
		}
		q.arn = aws.StringValue(arn)
	}
	return q.arn, nil
}

// Subscribe the queue to an SNS topic
func (q *Queue) Subscribe() error {
	arn, err := q.getArn()
	if err != nil {
		return err
	}
	out, err := q.snsClient.Subscribe(&sns.SubscribeInput{
		TopicArn: aws.String(q.topicArn),
		Protocol: aws.String("sqs"),
		Endpoint: aws.String(arn),
	})
	if err != nil {
		return err
	}
	q.subscriptionArn = aws.StringValue(out.SubscriptionArn)
	return nil
}

// GetMessages long polls for messages from the SQS queue.
func (q *Queue) GetMessages(ctx context.Context) ([]*sqs.Message, error) {
	out, err := q.sqsClient.ReceiveMessageWithContext(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(q.url),
		MaxNumberOfMessages: aws.Int64(1),
		WaitTimeSeconds:     aws.Int64(longPollingWaitTimeSeconds),
		VisibilityTimeout:   aws.Int64(0),
	})
	if err != nil {
		// Ignore error if the context was cancelled (i.e. we are shutting down)
		if e, ok := err.(awserr.Error); ok && e.Code() == request.CanceledErrorCode {
			return nil, nil
		}
		return nil, err
	}
	return out.Messages, nil
}

// DeleteMessage from the queue.
func (q *Queue) DeleteMessage(ctx context.Context, receiptHandle string) error {
	_, err := q.sqsClient.DeleteMessageWithContext(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(q.url),
		ReceiptHandle: aws.String(receiptHandle),
	})
	if err != nil {
		if e, ok := err.(awserr.Error); ok && e.Code() == request.CanceledErrorCode {
			return nil
		}
		return err
	}
	return nil
}

// Unsubscribe the queue from the SNS topic.
func (q *Queue) Unsubscribe() error {
	_, err := q.snsClient.Unsubscribe(&sns.UnsubscribeInput{
		SubscriptionArn: aws.String(q.subscriptionArn),
	})
	return err
}

// Delete the SQS queue.
func (q *Queue) Delete() error {
	_, err := q.sqsClient.DeleteQueue(&sqs.DeleteQueueInput{
		QueueUrl: aws.String(q.url),
	})
	if err != nil {
		// Ignore error if queue does not exist (which is what we want)
		if e, ok := err.(awserr.Error); !ok || e.Code() != sqs.ErrCodeQueueDoesNotExist {
			return err
		}
	}
	return nil
}
