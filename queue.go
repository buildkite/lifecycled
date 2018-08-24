package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	log "github.com/Sirupsen/logrus"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/sns"
	"github.com/aws/aws-sdk-go/service/sqs"
)

const queuePolicy = `
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

const (
	longPollingWaitTimeSeconds = 20
)

type Queue struct {
	name         string
	url          string
	arn          string
	subscription string
	session      *session.Session
}

func CreateQueue(sess *session.Session, queueName string, topicARN string) (*Queue, error) {
	sqsAPI := sqs.New(sess)
	snsAPI := sns.New(sess)

	log.WithFields(log.Fields{"queue": queueName}).Debug("Creating sqs queue")
	resp, err := sqsAPI.CreateQueue(&sqs.CreateQueueInput{
		QueueName: aws.String(queueName),
		Attributes: map[string]*string{
			"Policy":                        aws.String(fmt.Sprintf(queuePolicy, topicARN)),
			"ReceiveMessageWaitTimeSeconds": aws.String(strconv.Itoa(longPollingWaitTimeSeconds)),
		},
	})
	if err != nil {
		return nil, err
	}

	log.WithFields(log.Fields{"queue": queueName}).Debug("Looking up sqs queue url")
	attrs, err := sqsAPI.GetQueueAttributes(&sqs.GetQueueAttributesInput{
		AttributeNames: aws.StringSlice([]string{"QueueArn"}),
		QueueUrl:       resp.QueueUrl,
	})
	if err != nil {
		return nil, err
	}

	arn, ok := attrs.Attributes["QueueArn"]
	if !ok {
		return nil, errors.New("No attribute QueueArn")
	}

	log.WithFields(log.Fields{"queue": queueName, "topic": topicARN}).Debug("Subscribing queue to sns topic")
	subscr, err := snsAPI.Subscribe(&sns.SubscribeInput{
		Protocol: aws.String("sqs"),
		TopicArn: aws.String(topicARN),
		Endpoint: arn,
	})
	if err != nil {
		return nil, err
	}

	return &Queue{
		name:         queueName,
		url:          *resp.QueueUrl,
		subscription: *subscr.SubscriptionArn,
		arn:          *arn,
		session:      sess,
	}, nil
}

func (q *Queue) Receive(ctx context.Context, ch chan *sqs.Message) error {
	// Close channel before returning since this is the sending side.
	defer close(ch)
	log.WithFields(log.Fields{"queueURL": q.url}).Debugf("Polling sqs for messages")

	sqsAPI := sqs.New(q.session)

Loop:
	for {
		select {
		case <-ctx.Done():
			break Loop
		default:
			resp, err := sqsAPI.ReceiveMessageWithContext(ctx, &sqs.ReceiveMessageInput{
				QueueUrl:            aws.String(q.url),
				MaxNumberOfMessages: aws.Int64(1),
				WaitTimeSeconds:     aws.Int64(longPollingWaitTimeSeconds),
				VisibilityTimeout:   aws.Int64(0),
			})
			if err != nil {
				// Ignore error if the context was cancelled (i.e. we are shutting down)
				if e, ok := err.(awserr.Error); ok && e.Code() == request.CanceledErrorCode {
					return nil
				}
				return err
			}
			for _, m := range resp.Messages {
				sqsAPI.DeleteMessageWithContext(ctx, &sqs.DeleteMessageInput{
					QueueUrl:      aws.String(q.url),
					ReceiptHandle: m.ReceiptHandle,
				})
				ch <- m
			}
		}
	}
	return nil
}

func (q *Queue) Delete() error {
	sqsAPI := sqs.New(q.session)
	snsAPI := sns.New(q.session)

	log.WithFields(log.Fields{"arn": q.subscription}).Debugf("Deleting sns subscription")
	_, err := snsAPI.Unsubscribe(&sns.UnsubscribeInput{
		SubscriptionArn: aws.String(q.subscription),
	})
	if err != nil {
		return err
	}

	log.WithFields(log.Fields{"url": q.url}).Debugf("Deleting sqs queue")
	_, err = sqsAPI.DeleteQueue(&sqs.DeleteQueueInput{
		QueueUrl: aws.String(q.url),
	})
	return err
}
