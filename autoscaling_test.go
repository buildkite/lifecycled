package lifecycled

import (
	"context"
	"sync/atomic"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// stubSQSClient drives the autoscaling listener's queue without real AWS. Create
// and Subscribe always succeed; ReceiveMessage returns receiveErr and counts how
// often it was called; DeleteQueue returns deleteQueueErr so queue tests can
// exercise its error paths.
type stubSQSClient struct {
	receiveErr     error
	receiveCalls   int64
	deleteQueueErr error
}

func (s *stubSQSClient) CreateQueue(context.Context, *sqs.CreateQueueInput, ...func(*sqs.Options)) (*sqs.CreateQueueOutput, error) {
	return &sqs.CreateQueueOutput{QueueUrl: aws.String("url")}, nil
}

func (s *stubSQSClient) GetQueueAttributes(context.Context, *sqs.GetQueueAttributesInput, ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
	return &sqs.GetQueueAttributesOutput{Attributes: map[string]string{"QueueArn": "arn"}}, nil
}

func (s *stubSQSClient) ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	atomic.AddInt64(&s.receiveCalls, 1)
	if s.receiveErr != nil {
		return nil, s.receiveErr
	}
	return &sqs.ReceiveMessageOutput{}, nil
}

func (s *stubSQSClient) DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	return &sqs.DeleteMessageOutput{}, nil
}

func (s *stubSQSClient) DeleteQueue(context.Context, *sqs.DeleteQueueInput, ...func(*sqs.Options)) (*sqs.DeleteQueueOutput, error) {
	if s.deleteQueueErr != nil {
		return nil, s.deleteQueueErr
	}
	return &sqs.DeleteQueueOutput{}, nil
}

type stubSNSClient struct{}

func (stubSNSClient) Subscribe(context.Context, *sns.SubscribeInput, ...func(*sns.Options)) (*sns.SubscribeOutput, error) {
	return &sns.SubscribeOutput{SubscriptionArn: aws.String("arn")}, nil
}

func (stubSNSClient) Unsubscribe(context.Context, *sns.UnsubscribeInput, ...func(*sns.Options)) (*sns.UnsubscribeOutput, error) {
	return &sns.UnsubscribeOutput{}, nil
}
