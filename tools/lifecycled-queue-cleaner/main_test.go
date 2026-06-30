package main

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"
)

func TestFilterInactiveQueues(t *testing.T) {
	const (
		dead    = "https://sqs.us-east-1.amazonaws.com/123456789012/lifecycled-i-dead"
		running = "https://sqs.us-east-1.amazonaws.com/123456789012/lifecycled-i-running"
		other   = "https://sqs.us-east-1.amazonaws.com/123456789012/some-other-queue"
		chinaCN = "https://sqs.cn-north-1.amazonaws.com.cn/123456789012/lifecycled-i-dead"
	)
	runningSet := map[string]struct{}{"i-running": {}}

	tests := []struct {
		name string
		urls []string
		want []string
	}{
		{name: "empty input", urls: nil, want: nil},
		{name: "keeps queue for terminated instance", urls: []string{dead}, want: []string{dead}},
		{name: "skips queue for running instance", urls: []string{running}, want: nil},
		{name: "ignores non-lifecycled queue", urls: []string{other}, want: nil},
		{name: "matches china partition url", urls: []string{chinaCN}, want: []string{chinaCN}},
		{name: "mixed", urls: []string{dead, running, other}, want: []string{dead}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := filterInactiveQueues(tt.urls, runningSet); !slices.Equal(got, tt.want) {
				t.Errorf("filterInactiveQueues() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAccountMatches(t *testing.T) {
	tests := []struct {
		name     string
		expected string
		resolved string
		want     bool
	}{
		{name: "no guard requested", expected: "", resolved: "123456789012", want: true},
		{name: "match", expected: "123456789012", resolved: "123456789012", want: true},
		{name: "mismatch", expected: "123456789012", resolved: "999999999999", want: false},
		{name: "guard set but account unresolved aborts", expected: "123456789012", resolved: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := accountMatches(tt.expected, tt.resolved); got != tt.want {
				t.Errorf("accountMatches(%q, %q) = %v, want %v", tt.expected, tt.resolved, got, tt.want)
			}
		})
	}
}

func TestExpiredCredential(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "non-API error", err: errors.New("boom"), want: false},
		{name: "other API error", err: &smithy.GenericAPIError{Code: "AccessDenied"}, want: false},
		{name: "ExpiredToken", err: &smithy.GenericAPIError{Code: "ExpiredToken"}, want: true},
		{name: "ExpiredTokenException", err: &smithy.GenericAPIError{Code: "ExpiredTokenException"}, want: true},
		{name: "RequestExpired", err: &smithy.GenericAPIError{Code: "RequestExpired"}, want: true},
		{name: "SSOProviderInvalidToken", err: &smithy.GenericAPIError{Code: "SSOProviderInvalidToken"}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, got := expiredCredential(tt.err); got != tt.want {
				t.Errorf("expiredCredential() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeleteQueues(t *testing.T) {
	sentinel := errors.New("delete failed")
	tests := []struct {
		name      string
		queues    []string
		parallel  int
		failOn    string
		wantCount uint64
		wantErr   error
	}{
		{
			name:      "deletes all queues",
			queues:    []string{"q1", "q2", "q3", "q4", "q5"},
			parallel:  3,
			wantCount: 5,
		},
		{
			name:     "no queues",
			queues:   nil,
			parallel: 3,
		},
		{
			name:      "fewer queues than workers",
			queues:    []string{"q1", "q2"},
			parallel:  10,
			wantCount: 2,
		},
		{
			name:      "single worker",
			queues:    []string{"q1", "q2", "q3"},
			parallel:  1,
			wantCount: 3,
		},
		{
			name:      "non-positive parallel is clamped to one worker",
			queues:    []string{"q1", "q2", "q3"},
			parallel:  0,
			wantCount: 3,
		},
		{
			name:     "surfaces a delete error",
			queues:   []string{"q1", "q2", "q3"},
			parallel: 2,
			failOn:   "q2",
			wantErr:  sentinel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var deleted int64
			count, err := deleteQueues(tt.queues, tt.parallel, func(queue string) error {
				atomic.AddInt64(&deleted, 1)
				if tt.failOn != "" && queue == tt.failOn {
					return sentinel
				}
				return nil
			})

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			if count != tt.wantCount {
				t.Errorf("count = %d, want %d", count, tt.wantCount)
			}
			if int64(count) != deleted {
				t.Errorf("count = %d but deleteFn ran %d times", count, deleted)
			}
		})
	}
}

type fakeSNS struct {
	subs         []snstypes.Subscription
	existing     map[string]bool
	topicErr     map[string]error
	topicCalls   int
	unsubscribed []string
}

func (f *fakeSNS) ListSubscriptions(_ context.Context, _ *sns.ListSubscriptionsInput, _ ...func(*sns.Options)) (*sns.ListSubscriptionsOutput, error) {
	return &sns.ListSubscriptionsOutput{Subscriptions: f.subs}, nil
}

func (f *fakeSNS) GetTopicAttributes(_ context.Context, in *sns.GetTopicAttributesInput, _ ...func(*sns.Options)) (*sns.GetTopicAttributesOutput, error) {
	f.topicCalls++
	arn := aws.ToString(in.TopicArn)
	if err, ok := f.topicErr[arn]; ok {
		return nil, err
	}
	if f.existing[arn] {
		return &sns.GetTopicAttributesOutput{}, nil
	}
	return nil, &smithy.GenericAPIError{Code: "NotFound", Message: "topic not found"}
}

func (f *fakeSNS) Unsubscribe(_ context.Context, in *sns.UnsubscribeInput, _ ...func(*sns.Options)) (*sns.UnsubscribeOutput, error) {
	f.unsubscribed = append(f.unsubscribed, aws.ToString(in.SubscriptionArn))
	return &sns.UnsubscribeOutput{}, nil
}

func newSub(endpoint, topicArn, subArn string) snstypes.Subscription {
	return snstypes.Subscription{
		Endpoint:        aws.String(endpoint),
		TopicArn:        aws.String(topicArn),
		SubscriptionArn: aws.String(subArn),
	}
}

func TestListInactiveSubscriptions(t *testing.T) {
	fake := &fakeSNS{
		subs: []snstypes.Subscription{
			newSub("arn:aws:sqs:us-east-1:123456789012:lifecycled-i-dead", "topic-gone", "sub-dead"),
			newSub("arn:aws:sqs:us-east-1:123456789012:lifecycled-i-dead2", "topic-gone", "sub-dead2"),
			newSub("arn:aws:sqs:us-east-1:123456789012:lifecycled-i-live", "topic-live", "sub-live"),
			newSub("arn:aws:sqs:us-east-1:123456789012:some-other-queue", "topic-gone", "sub-other"),
		},
		existing: map[string]bool{"topic-live": true},
	}

	got, err := listInactiveSubscriptions(context.Background(), fake)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	want := []string{"sub-dead", "sub-dead2"}
	if !slices.Equal(got, want) {
		t.Errorf("listInactiveSubscriptions() = %v, want %v", got, want)
	}
	// topic-gone and topic-live are queried once each; the second lifecycled-i
	// subscription on topic-gone is answered from the memo, and the non-lifecycled
	// endpoint is filtered out before any topic lookup.
	if fake.topicCalls != 2 {
		t.Errorf("GetTopicAttributes calls = %d, want 2 (memoized per topic)", fake.topicCalls)
	}
}

func TestDeleteInactiveSubscriptions(t *testing.T) {
	fake := &fakeSNS{
		subs: []snstypes.Subscription{
			newSub("arn:aws:sqs:us-east-1:123456789012:lifecycled-i-dead", "topic-gone", "sub-dead"),
			newSub("arn:aws:sqs:us-east-1:123456789012:lifecycled-i-live", "topic-live", "sub-live"),
		},
		existing: map[string]bool{"topic-live": true},
	}

	count, err := deleteInactiveSubscriptions(context.Background(), fake)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
	if !slices.Equal(fake.unsubscribed, []string{"sub-dead"}) {
		t.Errorf("unsubscribed = %v, want [sub-dead]", fake.unsubscribed)
	}
}

// A non-NotFound topic lookup failure must abort the listing, not silently queue
// the subscription for deletion as if the topic were gone.
func TestListInactiveSubscriptionsAbortsOnTopicError(t *testing.T) {
	sentinel := &smithy.GenericAPIError{Code: "Throttling", Message: "rate exceeded"}
	fake := &fakeSNS{
		subs: []snstypes.Subscription{
			newSub("arn:aws:sqs:us-east-1:123456789012:lifecycled-i-dead", "topic-err", "sub-dead"),
		},
		topicErr: map[string]error{"topic-err": sentinel},
	}

	got, err := listInactiveSubscriptions(context.Background(), fake)
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want it to wrap %v", err, sentinel)
	}
	if got != nil {
		t.Errorf("subscriptions = %v, want nil (a topic-lookup error must abort, not queue deletions)", got)
	}
}

type fakeSQS struct {
	pages      [][]string // queue-URL pages returned in order
	listInputs []*sqs.ListQueuesInput
	deleted    []string
	deleteErr  map[string]error // per-URL DeleteQueue error
}

func (f *fakeSQS) ListQueues(_ context.Context, in *sqs.ListQueuesInput, _ ...func(*sqs.Options)) (*sqs.ListQueuesOutput, error) {
	f.listInputs = append(f.listInputs, in)
	page := 0
	if in.NextToken != nil {
		page, _ = strconv.Atoi(aws.ToString(in.NextToken))
	}
	out := &sqs.ListQueuesOutput{QueueUrls: f.pages[page]}
	// Mimic SQS: a NextToken is only handed out when MaxResults is set.
	if in.MaxResults != nil && page+1 < len(f.pages) {
		out.NextToken = aws.String(strconv.Itoa(page + 1))
	}
	return out, nil
}

func (f *fakeSQS) DeleteQueue(_ context.Context, in *sqs.DeleteQueueInput, _ ...func(*sqs.Options)) (*sqs.DeleteQueueOutput, error) {
	url := aws.ToString(in.QueueUrl)
	f.deleted = append(f.deleted, url)
	if err := f.deleteErr[url]; err != nil {
		return nil, err
	}
	return &sqs.DeleteQueueOutput{}, nil
}

// listQueues must follow NextToken across every page. Without MaxResults set on
// the request, SQS caps at one page, so this also guards the MaxResults fix.
func TestListQueuesPaginates(t *testing.T) {
	fake := &fakeSQS{pages: [][]string{
		{"q1", "q2"},
		{"q3", "q4"},
		{"q5"},
	}}

	got, err := listQueues(context.Background(), fake)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	want := []string{"q1", "q2", "q3", "q4", "q5"}
	if !slices.Equal(got, want) {
		t.Errorf("listQueues() = %v, want %v", got, want)
	}
	if len(fake.listInputs) != len(fake.pages) {
		t.Fatalf("ListQueues calls = %d, want %d (one per page)", len(fake.listInputs), len(fake.pages))
	}
	if aws.ToInt32(fake.listInputs[0].MaxResults) != 1000 {
		t.Errorf("MaxResults = %d, want 1000", aws.ToInt32(fake.listInputs[0].MaxResults))
	}
}

func TestDeleteQueue(t *testing.T) {
	apiErr := &smithy.GenericAPIError{Code: "AccessDenied", Message: "denied"}
	tests := []struct {
		name    string
		err     error
		wantErr error
	}{
		{name: "success", err: nil},
		{name: "already gone is success", err: &sqstypes.QueueDoesNotExist{Message: aws.String("gone")}},
		{name: "other error propagates", err: apiErr, wantErr: apiErr},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeSQS{deleteErr: map[string]error{"q1": tt.err}}
			err := deleteQueue(context.Background(), fake, "q1")
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %s", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
