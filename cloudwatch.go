package lifecycled

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwltypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/smithy-go"
	"github.com/sirupsen/logrus"
)

// CloudWatchLogsClient is the subset of the CloudWatch Logs API the hook uses.
type CloudWatchLogsClient interface {
	CreateLogGroup(context.Context, *cloudwatchlogs.CreateLogGroupInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.CreateLogGroupOutput, error)
	CreateLogStream(context.Context, *cloudwatchlogs.CreateLogStreamInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.CreateLogStreamOutput, error)
	PutLogEvents(context.Context, *cloudwatchlogs.PutLogEventsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.PutLogEventsOutput, error)
}

// CloudWatchLogsHook is a logrus hook that ships log entries to a CloudWatch
// Logs stream. Entries are written synchronously so each line is delivered
// before the daemon continues, which matters when handling a termination notice
// that ends with the instance shutting down.
type CloudWatchLogsHook struct {
	client     CloudWatchLogsClient
	groupName  string
	streamName string
}

// NewCloudWatchLogsHook creates the log group and stream if they don't already
// exist and returns a hook that writes to them.
func NewCloudWatchLogsHook(ctx context.Context, client CloudWatchLogsClient, groupName, streamName string) (*CloudWatchLogsHook, error) {
	// CreateLogGroup is best-effort: log groups are commonly provisioned out of
	// band (e.g. Terraform) and the daemon may run without logs:CreateLogGroup. A
	// genuinely missing group still surfaces below, where CreateLogStream fails.
	if _, err := client.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{
		LogGroupName: aws.String(groupName),
	}); err != nil && !alreadyExists(err) && !accessDenied(err) {
		return nil, err
	}
	if _, err := client.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{
		LogGroupName:  aws.String(groupName),
		LogStreamName: aws.String(streamName),
	}); err != nil && !alreadyExists(err) {
		return nil, err
	}
	return &CloudWatchLogsHook{client: client, groupName: groupName, streamName: streamName}, nil
}

func alreadyExists(err error) bool {
	var e *cwltypes.ResourceAlreadyExistsException
	return errors.As(err, &e)
}

// accessDenied reports whether err is an IAM authorization failure, covering both
// "AccessDenied" and "AccessDeniedException" codes.
func accessDenied(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && strings.Contains(apiErr.ErrorCode(), "AccessDenied")
}

// Levels returns the log levels the hook fires on.
func (h *CloudWatchLogsHook) Levels() []logrus.Level {
	return []logrus.Level{
		logrus.PanicLevel,
		logrus.FatalLevel,
		logrus.ErrorLevel,
		logrus.WarnLevel,
		logrus.InfoLevel,
		logrus.DebugLevel,
	}
}

// Fire ships the formatted entry to CloudWatch Logs on a background context with
// a timeout, so a line is still delivered during shutdown without an unreachable
// endpoint wedging the logging goroutine.
func (h *CloudWatchLogsHook) Fire(entry *logrus.Entry) error {
	line, err := entry.String()
	if err != nil {
		return err
	}

	// logrus stamps entry.Time on every log call; fall back to now for a
	// hand-built entry so we never send a negative timestamp AWS would reject.
	ts := entry.Time
	if ts.IsZero() {
		ts = time.Now()
	}

	// No lock needed: Fire reads only immutable fields and PutLogEvents accepts
	// parallel calls on the same stream.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = h.client.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogGroupName:  aws.String(h.groupName),
		LogStreamName: aws.String(h.streamName),
		LogEvents: []cwltypes.InputLogEvent{{
			Message:   aws.String(line),
			Timestamp: aws.Int64(ts.UnixMilli()),
		}},
	})
	return err
}
