package lifecycled_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/sns"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/golang/mock/gomock"
	"github.com/itsdalmo/lifecycled"
	"github.com/itsdalmo/lifecycled/mocks"
)

func newSQSMessage(instanceID string) *sqs.Message {
	m := fmt.Sprintf(`
{
	"Time": "2016-02-26T21:09:59.517Z",
	"AutoscalingGroupName": "group",
	"EC2InstanceId": "%s",
	"LifecycleActionToken": "token",
	"LifecycleTransition": "autoscaling:EC2_INSTANCE_TERMINATING",
	"LifecycleHookName": "hook"
}
	`, instanceID)

	e, err := json.Marshal(&lifecycled.Envelope{
		Type:    "type",
		Subject: "subject",
		Time:    time.Now(),
		Message: m,
	})

	if err != nil {
		panic(err)
	}

	return &sqs.Message{
		Body:          aws.String(string(e)),
		ReceiptHandle: aws.String("handle"),
	}
}

func TestAutoscalingListener(t *testing.T) {
	tests := []struct {
		description  string
		instanceID   string
		expectNotice bool
		interrupt    bool
	}{
		{
			description:  "sends notice if a termination notice is found",
			instanceID:   "i-00000000000",
			expectNotice: true,
		},
		{
			description:  "can be interrupted by cancelling the context",
			instanceID:   "i-00000000000",
			interrupt:    true,
			expectNotice: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// SQS mock and expectations
			sq := mocks.NewMockSQSClient(ctrl)
			sq.EXPECT().CreateQueue(gomock.Any()).Times(1).Return(&sqs.CreateQueueOutput{
				QueueUrl: aws.String("url"),
			}, nil)
			sq.EXPECT().GetQueueAttributes(gomock.Any()).Times(1).Return(&sqs.GetQueueAttributesOutput{
				Attributes: map[string]*string{"QueueArn": aws.String("arn")},
			}, nil)
			sq.EXPECT().DeleteQueue(gomock.Any()).Times(1).Return(nil, nil)

			// SQS should have receive/delete requests if context is not cancelled
			if !tc.interrupt {
				sq.EXPECT().ReceiveMessageWithContext(gomock.Any(), gomock.Any()).MinTimes(1).Return(&sqs.ReceiveMessageOutput{
					Messages: []*sqs.Message{newSQSMessage(tc.instanceID)},
				}, nil)
				sq.EXPECT().DeleteMessageWithContext(gomock.Any(), gomock.Any()).MinTimes(1).Return(nil, nil)
			}

			// SNS mock and expectations
			sn := mocks.NewMockSNSClient(ctrl)
			sn.EXPECT().Subscribe(gomock.Any()).Times(1).Return(&sns.SubscribeOutput{
				SubscriptionArn: aws.String("arn"),
			}, nil)
			sn.EXPECT().Unsubscribe(gomock.Any()).Times(1).Return(nil, nil)

			// Autoscaling mock
			as := mocks.NewMockAutoscalingClient(ctrl)

			// Record whether or not a notice was recieved
			notices := make(chan lifecycled.TerminationNotice, 1)

			var receivedNotice bool
			var wg sync.WaitGroup

			wg.Add(1)
			go func() {
				defer wg.Done()
				for range notices {
					receivedNotice = true
					break
				}
			}()

			ctx, cancel := context.WithCancel(context.TODO())
			if tc.interrupt {
				cancel()
			} else {
				defer cancel()
			}

			listener := lifecycled.NewAutoscalingListener(
				tc.instanceID,
				lifecycled.NewQueue("queue-name", "topic-arn", sq, sn),
				as,
			)
			err := listener.Start(ctx, notices)
			if err != nil {
				t.Errorf("unexpected error: %s", err)

			}
			close(notices)

			wg.Wait()
			if tc.expectNotice && !receivedNotice {
				t.Errorf("expected to receive a notice")
			}
		})
	}
}

func TestAutoscalingQueueCleanup(t *testing.T) {
	tests := []struct {
		description string
		instanceID  string
	}{
		{
			description: "cleans up queue if it fails to subscribe",
			instanceID:  "i-00000000000",
		},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// SQS mock and expectations
			sq := mocks.NewMockSQSClient(ctrl)
			sq.EXPECT().CreateQueue(gomock.Any()).Times(1).Return(&sqs.CreateQueueOutput{
				QueueUrl: aws.String("url"),
			}, nil)
			sq.EXPECT().GetQueueAttributes(gomock.Any()).Times(1).Return(&sqs.GetQueueAttributesOutput{
				Attributes: map[string]*string{"QueueArn": aws.String("arn")},
			}, nil)
			sq.EXPECT().DeleteQueue(gomock.Any()).Times(1).Return(nil, nil)

			// SNS mock and expectations
			sn := mocks.NewMockSNSClient(ctrl)
			sn.EXPECT().Subscribe(gomock.Any()).Times(1).Return(nil, errors.New("test"))

			// Record whether or not a notice was recieved
			notices := make(chan lifecycled.TerminationNotice, 1)
			defer close(notices)

			listener := lifecycled.NewAutoscalingListener(
				tc.instanceID,
				lifecycled.NewQueue("queue-name", "topic-arn", sq, sn),
				mocks.NewMockAutoscalingClient(ctrl),
			)
			err := listener.Start(context.TODO(), notices)
			if err == nil {
				t.Errorf("expected an error to occur")
			}
		})
	}
}
