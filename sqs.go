package lifecycled

import (
	"encoding/json"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/sqs"
)

type sqsQueue struct {
	queueURL, instanceID string
	svc                  *sqs.SQS
}

func NewSQSQueue(queueURL, instanceID string) Queue {
	return &sqsQueue{
		svc:        sqs.New(session.New()),
		queueURL:   queueURL,
		instanceID: instanceID,
	}
}

func (sq *sqsQueue) Receive(ch chan Message, opts ReceiveOpts) error {
	for t := range time.NewTicker(time.Millisecond * 500).C {
		ch <- Message{
			Time:       t,
			Transition: instanceTerminatingEvent,
			InstanceID: sq.instanceID,
		}
	}
	return nil
}

func (sq *sqsQueue) Delete(m Message) error {
	return nil
}

func (sq *sqsQueue) Release(m Message) error {
	return nil
}

// 	sqsSvc := sqs.New(session.New())
// 	autoscaleSvc := autoscaling.New(session.New())

// 	var poller *pidPoller
// 	if *pid != 0 {
// 		poller = newPidPoller(*pid)
// 	} else if *pidFile != "" {
// 		poller = newPidFilePoller(*pidFile)
// 	} else {
// 		log.Fatal("Either pid or pidfile must be provided")
// 	}

// 	for {
// 		messages, err := receiveMessages(sqsSvc, *sqsQueue)
// 		if err != nil {
// 			log.Println(err)
// 			continue
// 		}

// 		for _, m := range messages {
// 			if !matchMessage(m.Event, *instanceID) {
// 				if err = releaseMessage(sqsSvc, *sqsQueue, m.Message); err != nil {
// 					log.Println(err)
// 				}
// 				continue
// 			}

// 			log.Printf("Handling %s event for %s", m.Event.LifecycleTransition, m.Event.EC2InstanceID)

// 			hbt := time.NewTicker(heartbeatFrequency)
// 			go func() {
// 				for _ = range hbt.C {
// 					log.Println("Heartbeat fired")
// 					if err := sendHeartbeat(autoscaleSvc, m.Event); err != nil {
// 						log.Println(err)
// 					}
// 				}
// 			}()

// 			log.Printf("Shutting down buildkite-agent")
// 			if err = poller.Shutdown(); err != nil {
// 				log.Println("Failed to shutdown buildkite-agent:", err)
// 			} else {
// 				log.Printf("Waiting for buildkite-agent to stop")
// 				poller.Wait()
// 			}

// 			hbt.Stop()

// 			log.Printf("Completing EC2 Lifecycle event")
// 			if err := completeLifecycle(autoscaleSvc, m.Event); err != nil {
// 				log.Println(err)
// 			}

// 			log.Printf("Deleting SQS message")
// 			if err = deleteMessage(sqsSvc, *sqsQueue, m.Message); err != nil {
// 				log.Println(err)
// 			}
// 		}
// 	}
// }

func receiveMessages(svc *sqs.SQS, queue string) (msgs []Message, err error) {
	resp, err := svc.ReceiveMessage(&sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(queue),
		MaxNumberOfMessages: aws.Int64(10),
		WaitTimeSeconds:     aws.Int64(20),
		VisibilityTimeout:   aws.Int64(60),
	})
	if err != nil {
		return nil, err
	}

	for _, m := range resp.Messages {
		em := Message{
			Envelope: m,
		}
		if err := json.Unmarshal([]byte(*m.Body), &em); err != nil {
			return nil, err
		}
		msgs = append(msgs, em)
	}

	return msgs, nil
}

func deleteMessage(svc *sqs.SQS, queue string, msg *sqs.Message) error {
	_, err := svc.DeleteMessage(&sqs.DeleteMessageInput{
		QueueUrl:      aws.String(queue),
		ReceiptHandle: msg.ReceiptHandle,
	})
	return err
}

func releaseMessage(svc *sqs.SQS, queue string, msg *sqs.Message) error {
	_, err := svc.ChangeMessageVisibility(&sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(queue),
		ReceiptHandle:     msg.ReceiptHandle,
		VisibilityTimeout: aws.Int64(0),
	})
	return err
}
