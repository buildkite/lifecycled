package lifecycled

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"runtime"
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

type recordingASGClient struct {
	completeErr error
}

func (c *recordingASGClient) CompleteLifecycleAction(ctx context.Context, _ *autoscaling.CompleteLifecycleActionInput, _ ...func(*autoscaling.Options)) (*autoscaling.CompleteLifecycleActionOutput, error) {
	c.completeErr = ctx.Err()
	return &autoscaling.CompleteLifecycleActionOutput{}, ctx.Err()
}

func (c *recordingASGClient) RecordLifecycleActionHeartbeat(context.Context, *autoscaling.RecordLifecycleActionHeartbeatInput, ...func(*autoscaling.Options)) (*autoscaling.RecordLifecycleActionHeartbeatOutput, error) {
	return &autoscaling.RecordLifecycleActionHeartbeatOutput{}, nil
}

// blockingHandler stands in for a drain script that runs until its context is
// cancelled, modelling a SIGINT/SIGTERM arriving mid-handle.
type blockingHandler struct{}

func (blockingHandler) Execute(ctx context.Context, _ ...string) error {
	<-ctx.Done()
	return ctx.Err()
}

// A signal cancels the handler context mid-drain, but the deferred
// CompleteLifecycleAction must still release the ASG hook on a live context;
// otherwise the instance sits in Terminating:Wait until the hook times out.
func TestAutoscalingNoticeCompletesOnCancellation(t *testing.T) {
	as := &recordingASGClient{}
	notice := &autoscalingTerminationNotice{
		noticeType:        "autoscaling",
		message:           &Message{GroupName: "g", HookName: "h", InstanceID: "i", ActionToken: "t"},
		autoscaling:       as,
		heartbeatInterval: time.Hour,
	}

	logger := logrus.New()
	logger.SetOutput(io.Discard)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	if err := notice.Handle(ctx, blockingHandler{}, logrus.NewEntry(logger)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Handle returned %v, want context.Canceled from the interrupted handler", err)
	}

	if errors.Is(as.completeErr, context.Canceled) {
		t.Error("CompleteLifecycleAction ran on a cancelled context; the ASG hook would not be released")
	}
}

// countingASGClient counts heartbeats so a test can assert the heartbeat
// goroutine stops once Handle returns.
type countingASGClient struct {
	heartbeats int64
}

func (c *countingASGClient) CompleteLifecycleAction(context.Context, *autoscaling.CompleteLifecycleActionInput, ...func(*autoscaling.Options)) (*autoscaling.CompleteLifecycleActionOutput, error) {
	return &autoscaling.CompleteLifecycleActionOutput{}, nil
}

func (c *countingASGClient) RecordLifecycleActionHeartbeat(context.Context, *autoscaling.RecordLifecycleActionHeartbeatInput, ...func(*autoscaling.Options)) (*autoscaling.RecordLifecycleActionHeartbeatOutput, error) {
	atomic.AddInt64(&c.heartbeats, 1)
	return &autoscaling.RecordLifecycleActionHeartbeatOutput{}, nil
}

// sleepHandler models a drain script that runs for a fixed time and returns,
// so the heartbeat goroutine emits a few beats before Handle returns.
type sleepHandler struct{ d time.Duration }

func (h sleepHandler) Execute(ctx context.Context, _ ...string) error {
	select {
	case <-time.After(h.d):
	case <-ctx.Done():
	}
	return nil
}

// The heartbeat goroutine must stop when Handle returns; a bare "for range
// ticker.C" would run forever since ticker.Stop doesn't close the channel.
func TestAutoscalingNoticeStopsHeartbeat(t *testing.T) {
	as := &countingASGClient{}
	notice := &autoscalingTerminationNotice{
		noticeType:        "autoscaling",
		message:           &Message{GroupName: "g", HookName: "h", InstanceID: "i", ActionToken: "t"},
		autoscaling:       as,
		heartbeatInterval: 5 * time.Millisecond,
	}

	logger := logrus.New()
	logger.SetOutput(io.Discard)

	before := runtime.NumGoroutine()

	if err := notice.Handle(context.Background(), sleepHandler{30 * time.Millisecond}, logrus.NewEntry(logger)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if atomic.LoadInt64(&as.heartbeats) == 0 {
		t.Fatal("expected at least one heartbeat while handling")
	}

	// The heartbeat goroutine must exit once Handle returns; the old bare
	// "for range ticker.C" left it parked on the channel forever (ticker.Stop
	// stops beats but doesn't close the channel). Poll so a not-yet-scheduled
	// exit doesn't flake.
	for i := 0; i < 100; i++ {
		if runtime.NumGoroutine() <= before {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("heartbeat goroutine still running after Handle returned (goroutines: before=%d, now=%d)", before, runtime.NumGoroutine())
}
