package main

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/aws/aws-sdk-go/aws"
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

func (q *Queue) Receive(ch chan *sqs.Message) error {
	sqsAPI := sqs.New(q.session)

	log.WithFields(log.Fields{"queueURL": q.url}).Debugf("Polling sqs for messages")
	for range time.NewTicker(time.Millisecond * 100).C {
		resp, err := sqsAPI.ReceiveMessage(&sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(q.url),
			MaxNumberOfMessages: aws.Int64(1),
			WaitTimeSeconds:     aws.Int64(0),
			VisibilityTimeout:   aws.Int64(0),
		})
		if err != nil {
			return err
		}
		for _, m := range resp.Messages {
			sqsAPI.DeleteMessage(&sqs.DeleteMessageInput{
				QueueUrl:      aws.String(q.url),
				ReceiptHandle: m.ReceiptHandle,
			})
			ch <- m
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
