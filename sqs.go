package lifecycled

import (
	"encoding/json"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/sqs"
)

type sqsQueue struct {
	queueURL string
	svc      *sqs.SQS
}

func NewSQSQueue(queueURL string) Queue {
	return &sqsQueue{
		svc:      sqs.New(session.New()),
		queueURL: queueURL,
	}
}

func (sq *sqsQueue) Receive(ch chan Message, opts ReceiveOpts) error {
	for _ = range time.NewTicker(time.Millisecond * 250).C {
		resp, err := sq.svc.ReceiveMessage(&sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(sq.queueURL),
			MaxNumberOfMessages: aws.Int64(1),
			WaitTimeSeconds:     aws.Int64(5),
			VisibilityTimeout:   aws.Int64(0),
		})
		if err != nil {
			return err
		}
		for _, m := range resp.Messages {
			em := Message{
				Envelope: m,
			}
			if err := json.Unmarshal([]byte(*m.Body), &em); err != nil {
				return err
			}
			ch <- em
		}
	}
	return nil
}

func (sq *sqsQueue) Delete(m Message) error {
	_, err := sq.svc.DeleteMessage(&sqs.DeleteMessageInput{
		QueueUrl:      aws.String(sq.queueURL),
		ReceiptHandle: m.Envelope.(*sqs.Message).ReceiptHandle,
	})
	return err
}

func (sq *sqsQueue) Release(m Message) error {
	_, err := sq.svc.ChangeMessageVisibility(&sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(sq.queueURL),
		ReceiptHandle:     m.Envelope.(*sqs.Message).ReceiptHandle,
		VisibilityTimeout: aws.Int64(0),
	})
	return err
}

func (sq *sqsQueue) String() string {
	return sq.queueURL
}
