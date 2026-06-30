package lifecycled

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/sirupsen/logrus"
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

// recordingSQSClient drives the autoscaling listener straight to a termination
// notice and records the context DeleteQueue ran with. DeleteQueue waits up to a
// short delay or ctx cancellation, mimicking a network round-trip the v2 SDK
// would abort on a cancelled context.
type recordingSQSClient struct {
	instanceID     string
	deleteQueueErr error
}

func (c *recordingSQSClient) CreateQueue(context.Context, *sqs.CreateQueueInput, ...func(*sqs.Options)) (*sqs.CreateQueueOutput, error) {
	return &sqs.CreateQueueOutput{QueueUrl: aws.String("url")}, nil
}

func (c *recordingSQSClient) GetQueueAttributes(context.Context, *sqs.GetQueueAttributesInput, ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
	return &sqs.GetQueueAttributesOutput{Attributes: map[string]string{"QueueArn": "arn"}}, nil
}

func (c *recordingSQSClient) ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	inner := `{"AutoScalingGroupName":"group","EC2InstanceId":"` + c.instanceID + `","LifecycleActionToken":"token","LifecycleTransition":"autoscaling:EC2_INSTANCE_TERMINATING","LifecycleHookName":"hook"}`
	env, _ := json.Marshal(&Envelope{Type: "t", Message: inner})
	return &sqs.ReceiveMessageOutput{Messages: []sqstypes.Message{{Body: aws.String(string(env)), ReceiptHandle: aws.String("h")}}}, nil
}

func (c *recordingSQSClient) DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	return &sqs.DeleteMessageOutput{}, nil
}

func (c *recordingSQSClient) DeleteQueue(ctx context.Context, _ *sqs.DeleteQueueInput, _ ...func(*sqs.Options)) (*sqs.DeleteQueueOutput, error) {
	select {
	case <-time.After(50 * time.Millisecond):
	case <-ctx.Done():
	}
	c.deleteQueueErr = ctx.Err()
	return &sqs.DeleteQueueOutput{}, ctx.Err()
}

type recordingSNSClient struct {
	unsubscribeErr error
}

func (c *recordingSNSClient) Subscribe(context.Context, *sns.SubscribeInput, ...func(*sns.Options)) (*sns.SubscribeOutput, error) {
	return &sns.SubscribeOutput{SubscriptionArn: aws.String("arn")}, nil
}

func (c *recordingSNSClient) Unsubscribe(ctx context.Context, _ *sns.UnsubscribeInput, _ ...func(*sns.Options)) (*sns.UnsubscribeOutput, error) {
	select {
	case <-time.After(50 * time.Millisecond):
	case <-ctx.Done():
	}
	c.unsubscribeErr = ctx.Err()
	return &sns.UnsubscribeOutput{}, ctx.Err()
}

type noopASGClient struct{}

func (noopASGClient) CompleteLifecycleAction(context.Context, *autoscaling.CompleteLifecycleActionInput, ...func(*autoscaling.Options)) (*autoscaling.CompleteLifecycleActionOutput, error) {
	return &autoscaling.CompleteLifecycleActionOutput{}, nil
}

func (noopASGClient) RecordLifecycleActionHeartbeat(context.Context, *autoscaling.RecordLifecycleActionHeartbeatInput, ...func(*autoscaling.Options)) (*autoscaling.RecordLifecycleActionHeartbeatOutput, error) {
	return &autoscaling.RecordLifecycleActionHeartbeatOutput{}, nil
}

// Receiving a notice cancels the listener context, but the deferred queue and
// subscription cleanup must still run on a live context so the per-instance SQS
// queue and SNS subscription are actually removed rather than orphaned.
func TestAutoscalingListenerCleanupSurvivesCancellation(t *testing.T) {
	const instanceID = "i-000000000000"
	sq := &recordingSQSClient{instanceID: instanceID}
	sn := &recordingSNSClient{}

	logger := logrus.New()
	logger.SetOutput(io.Discard)

	daemon := NewDaemon(
		&Config{InstanceID: instanceID, SNSTopic: "topic"},
		sq, sn, noopASGClient{}, &stubMetadataClient{}, logger,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	notice, err := daemon.Start(ctx)
	if err != nil {
		t.Fatalf("daemon start: %v", err)
	}
	if notice == nil || notice.Type() != "autoscaling" {
		t.Fatalf("expected an autoscaling notice, got %v", notice)
	}

	if errors.Is(sq.deleteQueueErr, context.Canceled) {
		t.Error("DeleteQueue ran on a cancelled context; the SQS queue would be orphaned")
	}
	if errors.Is(sn.unsubscribeErr, context.Canceled) {
		t.Error("Unsubscribe ran on a cancelled context; the SNS subscription would be orphaned")
	}
}
