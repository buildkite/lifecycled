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
	logrustest "github.com/sirupsen/logrus/hooks/test"
)

// stubSQSClient drives the autoscaling listener's queue without real AWS. Create
// always succeeds; ReceiveMessage returns receiveErr and counts how often it was
// called so a test can tell a backed-off loop from a spinning one; DeleteQueue
// returns deleteQueueErr, counts calls, and records whether its context carried a
// deadline so a test can assert cleanup runs on a bounded context.
type stubSQSClient struct {
	receiveErr             error
	receiveCalls           int64
	deleteQueueErr         error
	deleteQueueCalls       int64
	deleteQueueHadDeadline bool
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

func (s *stubSQSClient) DeleteQueue(ctx context.Context, _ *sqs.DeleteQueueInput, _ ...func(*sqs.Options)) (*sqs.DeleteQueueOutput, error) {
	_, s.deleteQueueHadDeadline = ctx.Deadline()
	atomic.AddInt64(&s.deleteQueueCalls, 1)
	if s.deleteQueueErr != nil {
		return nil, s.deleteQueueErr
	}
	return &sqs.DeleteQueueOutput{}, nil
}

// stubSNSClient counts Unsubscribe calls and records whether its context carried
// a deadline, so a test can assert the subscription teardown runs on a bounded
// context during shutdown.
type stubSNSClient struct {
	unsubscribeCalls       int64
	unsubscribeHadDeadline bool
}

func (*stubSNSClient) Subscribe(context.Context, *sns.SubscribeInput, ...func(*sns.Options)) (*sns.SubscribeOutput, error) {
	return &sns.SubscribeOutput{SubscriptionArn: aws.String("arn")}, nil
}

func (s *stubSNSClient) Unsubscribe(ctx context.Context, _ *sns.UnsubscribeInput, _ ...func(*sns.Options)) (*sns.UnsubscribeOutput, error) {
	_, s.unsubscribeHadDeadline = ctx.Deadline()
	atomic.AddInt64(&s.unsubscribeCalls, 1)
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

// batchSQSClient returns one batch holding another instance's event followed by
// the target instance's, then empty batches, so a test can prove the listener
// scans the whole batch rather than only the first message. It also records the
// last MaxNumberOfMessages it was asked for so a test can assert the batch size.
type batchSQSClient struct {
	match       string
	received    int64
	deletes     int64
	maxMessages int64
}

func (c *batchSQSClient) CreateQueue(context.Context, *sqs.CreateQueueInput, ...func(*sqs.Options)) (*sqs.CreateQueueOutput, error) {
	return &sqs.CreateQueueOutput{QueueUrl: aws.String("url")}, nil
}

func (c *batchSQSClient) GetQueueAttributes(context.Context, *sqs.GetQueueAttributesInput, ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
	return &sqs.GetQueueAttributesOutput{Attributes: map[string]string{"QueueArn": "arn"}}, nil
}

func (c *batchSQSClient) ReceiveMessage(_ context.Context, in *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	atomic.StoreInt64(&c.maxMessages, int64(in.MaxNumberOfMessages))
	if atomic.AddInt64(&c.received, 1) != 1 {
		return &sqs.ReceiveMessageOutput{}, nil
	}
	return &sqs.ReceiveMessageOutput{Messages: []sqstypes.Message{
		batchMessage("i-999999999999", "h1"),
		batchMessage(c.match, "h2"),
	}}, nil
}

func (c *batchSQSClient) DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	atomic.AddInt64(&c.deletes, 1)
	return &sqs.DeleteMessageOutput{}, nil
}

func (c *batchSQSClient) DeleteQueue(context.Context, *sqs.DeleteQueueInput, ...func(*sqs.Options)) (*sqs.DeleteQueueOutput, error) {
	return &sqs.DeleteQueueOutput{}, nil
}

func batchMessage(instanceID, handle string) sqstypes.Message {
	inner := `{"AutoScalingGroupName":"group","EC2InstanceId":"` + instanceID + `","LifecycleActionToken":"token","LifecycleTransition":"autoscaling:EC2_INSTANCE_TERMINATING","LifecycleHookName":"hook"}`
	env, _ := json.Marshal(&Envelope{Type: "t", Message: inner})
	return sqstypes.Message{Body: aws.String(string(env)), ReceiptHandle: aws.String(handle)}
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

// failingHandler returns a genuine error immediately, modelling a drain script
// that fails on its own merits while the context is still live.
type failingHandler struct{ err error }

func (h failingHandler) Execute(context.Context, ...string) error { return h.err }

// cancelOnCompleteASGClient cancels the daemon context from inside
// CompleteLifecycleAction, modelling a SIGTERM that lands during the detached
// cleanup that runs after the handler has already failed.
type cancelOnCompleteASGClient struct{ cancel context.CancelFunc }

func (c cancelOnCompleteASGClient) CompleteLifecycleAction(context.Context, *autoscaling.CompleteLifecycleActionInput, ...func(*autoscaling.Options)) (*autoscaling.CompleteLifecycleActionOutput, error) {
	c.cancel()
	return &autoscaling.CompleteLifecycleActionOutput{}, nil
}

func (cancelOnCompleteASGClient) RecordLifecycleActionHeartbeat(context.Context, *autoscaling.RecordLifecycleActionHeartbeatInput, ...func(*autoscaling.Options)) (*autoscaling.RecordLifecycleActionHeartbeatOutput, error) {
	return &autoscaling.RecordLifecycleActionHeartbeatOutput{}, nil
}

// A handler that fails on its own merits must stay a genuine failure even when a
// SIGTERM cancels the context during the detached CompleteLifecycleAction that
// runs after the handler returns. Handle snapshots cancellation when the handler
// returns, so the up-to-awsActionTimeout cleanup window can't relabel a real
// failure as an interrupt and exit 0.
func TestAutoscalingNoticeFailureSurvivesCancellationDuringCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handlerErr := errors.New("drain failed")
	notice := &autoscalingTerminationNotice{
		noticeType:        "autoscaling",
		message:           &Message{GroupName: "g", HookName: "h", InstanceID: "i", ActionToken: "t"},
		autoscaling:       cancelOnCompleteASGClient{cancel: cancel},
		heartbeatInterval: time.Hour,
	}

	logger := logrus.New()
	logger.SetOutput(io.Discard)

	err := notice.Handle(ctx, failingHandler{err: handlerErr}, logrus.NewEntry(logger))
	if !errors.Is(err, handlerErr) {
		t.Fatalf("Handle returned %v, want the handler error", err)
	}
	if errors.Is(err, ErrDrainInterrupted) {
		t.Error("handler failure relabelled as interrupt after cancellation during cleanup; the process would exit 0")
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
	queue := NewQueue("queue", "topic", sqsStub, &stubSNSClient{}, "")
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

// Cleanup must run even after the parent context is cancelled during shutdown, and
// it must run on a fresh bounded context so a slow endpoint can't wedge shutdown
// or (on the notice path) delay the handler.
func TestAutoscalingListenerCleanupRunsOnBoundedContext(t *testing.T) {
	sqsStub := &stubSQSClient{}
	snsStub := &stubSNSClient{}
	queue := NewQueue("queue", "topic", sqsStub, snsStub, "")
	listener := NewAutoscalingListener("i-1234567890", queue, &stubAutoscalingClient{}, time.Minute)

	logger, _ := logrustest.NewNullLogger()
	notices := make(chan TerminationNotice, 1)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- listener.Start(ctx, notices, logrus.NewEntry(logger)) }()

	// Let the listener get past Create/Subscribe into the poll loop, so the cleanup
	// defer is registered with subscribed == true before we cancel.
	waitFor(t, func() bool { return atomic.LoadInt64(&sqsStub.receiveCalls) >= 1 })
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after cancel")
	}

	if got := atomic.LoadInt64(&snsStub.unsubscribeCalls); got != 1 {
		t.Errorf("Unsubscribe called %d times, want 1; cleanup must run after ctx is cancelled", got)
	}
	if got := atomic.LoadInt64(&sqsStub.deleteQueueCalls); got != 1 {
		t.Errorf("DeleteQueue called %d times, want 1; cleanup must run after ctx is cancelled", got)
	}
	if !snsStub.unsubscribeHadDeadline {
		t.Error("Unsubscribe context had no deadline; cleanup must be bounded by a timeout")
	}
	if !sqsStub.deleteQueueHadDeadline {
		t.Error("DeleteQueue context had no deadline; cleanup must be bounded by a timeout")
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

// A single ReceiveMessage batch can carry several instances' events under SNS
// fan-out, so the listener must scan the whole batch and emit for the matching
// instance even when it isn't the first message.
func TestAutoscalingListenerScansMessageBatch(t *testing.T) {
	const instanceID = "i-000000000000"
	sq := &batchSQSClient{match: instanceID}
	queue := NewQueue("queue", "topic", sq, &stubSNSClient{}, "")
	listener := NewAutoscalingListener(instanceID, queue, &stubAutoscalingClient{}, time.Minute)

	logger, _ := logrustest.NewNullLogger()
	notices := make(chan TerminationNotice, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := listener.Start(ctx, notices, logrus.NewEntry(logger)); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	select {
	case n := <-notices:
		if got := n.Type(); got != "autoscaling" {
			t.Errorf("notice type = %q, want %q", got, "autoscaling")
		}
	default:
		t.Fatal("expected a notice from the matching message later in the batch, got none")
	}

	// Both the non-matching message and the match ahead of the return are deleted,
	// proving the loop walked past the first message.
	if got := atomic.LoadInt64(&sq.deletes); got < 2 {
		t.Errorf("DeleteMessage calls = %d, want >= 2 (the whole batch up to the match is scanned)", got)
	}

	// The poll must request a full batch; at MaxNumberOfMessages: 1 the fan-out
	// backlog would drain one round-trip at a time, which is the regression here.
	if got := atomic.LoadInt64(&sq.maxMessages); got != 10 {
		t.Errorf("ReceiveMessage MaxNumberOfMessages = %d, want 10", got)
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
