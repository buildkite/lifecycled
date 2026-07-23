package lifecycled

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwltypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/smithy-go"
	"github.com/sirupsen/logrus"
)

type fakeCloudWatchLogsClient struct {
	mu             sync.Mutex
	createdGroups  []string
	createdStreams []string
	putInputs      []*cloudwatchlogs.PutLogEventsInput

	groupErr  error
	streamErr error
	putErr    error

	lastDeadline    time.Time
	lastHasDeadline bool
}

func (c *fakeCloudWatchLogsClient) CreateLogGroup(_ context.Context, in *cloudwatchlogs.CreateLogGroupInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.CreateLogGroupOutput, error) {
	c.createdGroups = append(c.createdGroups, aws.ToString(in.LogGroupName))
	return &cloudwatchlogs.CreateLogGroupOutput{}, c.groupErr
}

func (c *fakeCloudWatchLogsClient) CreateLogStream(_ context.Context, in *cloudwatchlogs.CreateLogStreamInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.CreateLogStreamOutput, error) {
	c.createdStreams = append(c.createdStreams, aws.ToString(in.LogStreamName))
	return &cloudwatchlogs.CreateLogStreamOutput{}, c.streamErr
}

func (c *fakeCloudWatchLogsClient) PutLogEvents(ctx context.Context, in *cloudwatchlogs.PutLogEventsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.PutLogEventsOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.putInputs = append(c.putInputs, in)
	c.lastDeadline, c.lastHasDeadline = ctx.Deadline()
	return &cloudwatchlogs.PutLogEventsOutput{}, c.putErr
}

func TestNewCloudWatchLogsHook(t *testing.T) {
	exists := &cwltypes.ResourceAlreadyExistsException{Message: aws.String("exists")}

	tests := []struct {
		name      string
		groupErr  error
		streamErr error
		wantErr   bool
	}{
		{
			name: "creates group and stream",
		},
		{
			name:      "tolerates pre-existing group and stream",
			groupErr:  exists,
			streamErr: exists,
		},
		{
			// Externally-managed log group + no logs:CreateLogGroup permission must
			// not be fatal; CreateLogStream still gates a genuinely missing group.
			name:     "tolerates access denied on group",
			groupErr: &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "no perms"},
		},
		{
			name:     "propagates other group errors",
			groupErr: errors.New("boom"),
			wantErr:  true,
		},
		{
			// A pre-provisioned stream + a role without logs:CreateLogStream must not
			// be fatal, mirroring the group tolerance above.
			name:      "tolerates access denied on stream",
			streamErr: &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "no perms"},
		},
		{
			name:      "propagates other stream errors",
			streamErr: errors.New("access denied"),
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakeCloudWatchLogsClient{groupErr: tt.groupErr, streamErr: tt.streamErr}
			hook, err := NewCloudWatchLogsHook(context.Background(), client, "group", "stream")

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			if hook == nil {
				t.Fatal("expected a hook, got nil")
			}
			if got := client.createdGroups; len(got) != 1 || got[0] != "group" {
				t.Errorf("created groups = %v, want [group]", got)
			}
			if got := client.createdStreams; len(got) != 1 || got[0] != "stream" {
				t.Errorf("created streams = %v, want [stream]", got)
			}
		})
	}
}

func TestCloudWatchLogsHookFireError(t *testing.T) {
	sentinel := errors.New("put failed")
	client := &fakeCloudWatchLogsClient{putErr: sentinel}
	hook, err := NewCloudWatchLogsHook(context.Background(), client, "group", "stream")
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	entry := logrus.NewEntry(logrus.New())
	entry.Message = "boom"
	if err := hook.Fire(entry); !errors.Is(err, sentinel) {
		t.Errorf("Fire() error = %v, want %v", err, sentinel)
	}
}

func TestCloudWatchLogsHookFire(t *testing.T) {
	client := &fakeCloudWatchLogsClient{}
	hook, err := NewCloudWatchLogsHook(context.Background(), client, "group", "stream")
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	logger := logrus.New()
	logger.SetOutput(io.Discard)
	logger.AddHook(hook)
	logger.Info("hello cloudwatch")

	if len(client.putInputs) != 1 {
		t.Fatalf("PutLogEvents calls = %d, want 1", len(client.putInputs))
	}
	in := client.putInputs[0]
	if got := aws.ToString(in.LogGroupName); got != "group" {
		t.Errorf("log group = %q, want %q", got, "group")
	}
	if got := aws.ToString(in.LogStreamName); got != "stream" {
		t.Errorf("log stream = %q, want %q", got, "stream")
	}
	if len(in.LogEvents) != 1 {
		t.Fatalf("log events = %d, want 1", len(in.LogEvents))
	}
	event := in.LogEvents[0]
	if !strings.Contains(aws.ToString(event.Message), "hello cloudwatch") {
		t.Errorf("event message = %q, want it to contain %q", aws.ToString(event.Message), "hello cloudwatch")
	}
	if aws.ToInt64(event.Timestamp) <= 0 {
		t.Errorf("event timestamp = %d, want > 0", aws.ToInt64(event.Timestamp))
	}
}

// Fire bounds delivery with a timeout so an unreachable endpoint can't wedge it.
func TestCloudWatchLogsHookFireAppliesTimeout(t *testing.T) {
	client := &fakeCloudWatchLogsClient{}
	hook, err := NewCloudWatchLogsHook(context.Background(), client, "group", "stream")
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	entry := logrus.NewEntry(logrus.New())
	entry.Message = "boom"
	if err := hook.Fire(entry); err != nil {
		t.Fatalf("Fire() error = %v", err)
	}

	if !client.lastHasDeadline {
		t.Fatal("PutLogEvents context had no deadline; Fire must bound delivery with a timeout")
	}
	if remaining := time.Until(client.lastDeadline); remaining <= 0 || remaining > 5*time.Second {
		t.Errorf("deadline remaining = %s, want within (0, 5s]", remaining)
	}
}

// Concurrent Fire calls (the daemon runs several listeners) must all be delivered.
func TestCloudWatchLogsHookFireConcurrent(t *testing.T) {
	client := &fakeCloudWatchLogsClient{}
	hook, err := NewCloudWatchLogsHook(context.Background(), client, "group", "stream")
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			entry := logrus.NewEntry(logrus.New())
			entry.Message = "concurrent"
			entry.Time = time.Now()
			if err := hook.Fire(entry); err != nil {
				t.Errorf("Fire() error = %v", err)
			}
		}()
	}
	wg.Wait()

	if len(client.putInputs) != n {
		t.Errorf("PutLogEvents calls = %d, want %d", len(client.putInputs), n)
	}
}

// A hand-built entry has a zero Time; Fire must fall back to now rather than
// sending a negative timestamp AWS would reject.
func TestCloudWatchLogsHookFireZeroTime(t *testing.T) {
	client := &fakeCloudWatchLogsClient{}
	hook, err := NewCloudWatchLogsHook(context.Background(), client, "group", "stream")
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	entry := logrus.NewEntry(logrus.New())
	entry.Message = "boom"
	if err := hook.Fire(entry); err != nil {
		t.Fatalf("Fire() error = %v", err)
	}

	if len(client.putInputs) != 1 {
		t.Fatalf("PutLogEvents calls = %d, want 1", len(client.putInputs))
	}
	if got := aws.ToInt64(client.putInputs[0].LogEvents[0].Timestamp); got <= 0 {
		t.Errorf("timestamp = %d, want > 0 (zero entry.Time should fall back to now)", got)
	}
}
