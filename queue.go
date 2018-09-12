package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/sns"
	"github.com/aws/aws-sdk-go/service/sns/snsiface"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/aws/aws-sdk-go/service/sqs/sqsiface"
	log "github.com/sirupsen/logrus"
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

// SQSClient for testing purposes (TODO: Gomock).
type SQSClient sqsiface.SQSAPI

// SNSClient for testing purposes (TODO: Gomock).
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
func NewQueue(sess *session.Session, queueName, topicArn string) *Queue {
	return &Queue{
		name:      queueName,
		topicArn:  topicArn,
		sqsClient: sqs.New(sess),
		snsClient: sns.New(sess),
	}
}

// Create the SQS queue.
func (q *Queue) Create() error {
	log.WithFields(log.Fields{"queue": q.name}).Debug("Creating sqs queue")
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
		log.WithFields(log.Fields{"queue": q.name}).Debug("Looking up sqs queue arn")
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
	log.WithFields(log.Fields{"queue": q.name, "topic": q.topicArn}).Debug("Subscribing queue to sns topic")

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
	log.WithFields(log.Fields{"queueURL": q.url}).Debugf("Polling sqs for messages")
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
	log.WithFields(log.Fields{"arn": q.subscriptionArn}).Debugf("Deleting sns subscription")
	_, err := q.snsClient.Unsubscribe(&sns.UnsubscribeInput{
		SubscriptionArn: aws.String(q.subscriptionArn),
	})
	return err
}

// Delete the SQS queue.
func (q *Queue) Delete() error {
	log.WithFields(log.Fields{"url": q.url}).Debugf("Deleting sqs queue")
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
