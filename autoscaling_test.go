package lifecycled

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
)

// stubSQSClient drives the autoscaling listener's queue without real AWS. Create
// and Subscribe always succeed; ReceiveMessage returns receiveErr and counts how
// often it was called so a test can tell a backed-off loop from a spinning one;
// DeleteQueue returns deleteQueueErr so queue tests can exercise its error paths.
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

// stubAutoscalingClient counts heartbeat and completion calls and records whether
// the completion call was given a deadline.
type stubAutoscalingClient struct {
	heartbeats          int64
	completes           int64
	completeHadDeadline bool
}

func (s *stubAutoscalingClient) RecordLifecycleActionHeartbeat(context.Context, *autoscaling.RecordLifecycleActionHeartbeatInput, ...func(*autoscaling.Options)) (*autoscaling.RecordLifecycleActionHeartbeatOutput, error) {
	atomic.AddInt64(&s.heartbeats, 1)
	return &autoscaling.RecordLifecycleActionHeartbeatOutput{}, nil
}

func (s *stubAutoscalingClient) CompleteLifecycleAction(ctx context.Context, _ *autoscaling.CompleteLifecycleActionInput, _ ...func(*autoscaling.Options)) (*autoscaling.CompleteLifecycleActionOutput, error) {
	_, s.completeHadDeadline = ctx.Deadline()
	atomic.AddInt64(&s.completes, 1)
	return &autoscaling.CompleteLifecycleActionOutput{}, nil
}

// sleepHandler runs for d (or until the context is cancelled) so heartbeats have
// time to fire while the handler is executing.
type sleepHandler struct {
	d time.Duration
}

func (h sleepHandler) Execute(ctx context.Context, _ ...string) error {
	select {
	case <-time.After(h.d):
	case <-ctx.Done():
	}
	return nil
}

// A persistently failing GetMessages must back off rather than spin, and must
// return promptly when the context is cancelled mid-backoff.
func TestAutoscalingListenerBacksOffOnReceiveError(t *testing.T) {
	sqsStub := &stubSQSClient{receiveErr: errors.New("throttled")}
	queue := NewQueue("queue", "topic", sqsStub, stubSNSClient{}, "")
	listener := NewAutoscalingListener("i-1234567890", queue, &stubAutoscalingClient{}, time.Minute)

	logger, _ := logrustest.NewNullLogger()
	notices := make(chan TerminationNotice, 1)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- listener.Start(ctx, notices, logrus.NewEntry(logger)) }()

	// The first failure parks the loop in the backoff select; without the backoff
	// the loop would spin and rack up ReceiveMessage calls.
	waitFor(t, func() bool { return atomic.LoadInt64(&sqsStub.receiveCalls) >= 1 })
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt64(&sqsStub.receiveCalls); got > 2 {
		t.Errorf("ReceiveMessage called %d times; the backoff should leave the loop parked after the first failure", got)
	}

	// Cancelling must break the backoff select well before the 5s timer elapses.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after cancel; the backoff select may ignore ctx.Done()")
	}
}

// The heartbeat goroutine must stop once Handle returns, and the lifecycle action
// must be completed exactly once.
func TestAutoscalingNoticeStopsHeartbeatWhenHandleReturns(t *testing.T) {
	as := &stubAutoscalingClient{}
	notice := &autoscalingTerminationNotice{
		noticeType: "autoscaling",
		message: &Message{
			GroupName:   "group",
			HookName:    "hook",
			InstanceID:  "i-1234567890",
			ActionToken: "token",
			Transition:  "autoscaling:EC2_INSTANCE_TERMINATING",
		},
		autoscaling:       as,
		heartbeatInterval: 10 * time.Millisecond,
	}
	logger, _ := logrustest.NewNullLogger()

	if err := notice.Handle(context.Background(), sleepHandler{d: 60 * time.Millisecond}, logrus.NewEntry(logger)); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	during := atomic.LoadInt64(&as.heartbeats)
	if during == 0 {
		t.Fatal("expected at least one heartbeat while the handler was running")
	}

	// If the goroutine outlived Handle it would keep ticking; allow one racy tick
	// from the select that may already have been in flight when Handle returned.
	time.Sleep(60 * time.Millisecond)
	if after := atomic.LoadInt64(&as.heartbeats); after-during > 1 {
		t.Errorf("heartbeats kept firing after Handle returned (%d during, %d after); goroutine not stopped", during, after)
	}

	if got := atomic.LoadInt64(&as.completes); got != 1 {
		t.Errorf("CompleteLifecycleAction called %d times, want 1", got)
	}

	// The completion runs on a fresh context during shutdown; it must be bounded
	// by a timeout so an unreachable endpoint can't wedge the process.
	if !as.completeHadDeadline {
		t.Error("CompleteLifecycleAction context had no deadline; the completion call must be bounded by a timeout")
	}
}

// waitFor polls cond until it is true, failing the test if it does not happen
// within a couple of seconds. Lets timing tests advance on observed progress
// rather than fixed sleeps.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}
