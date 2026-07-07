package main

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"
)

// fakeSNS returns a single page of subscriptions and a fixed GetTopicAttributes
// outcome, so the subscription-cleanup logic can be tested without real AWS.
type fakeSNS struct {
	subs     []snstypes.Subscription
	topicErr error
}

func (f *fakeSNS) ListSubscriptions(context.Context, *sns.ListSubscriptionsInput, ...func(*sns.Options)) (*sns.ListSubscriptionsOutput, error) {
	return &sns.ListSubscriptionsOutput{Subscriptions: f.subs}, nil
}

func (f *fakeSNS) GetTopicAttributes(context.Context, *sns.GetTopicAttributesInput, ...func(*sns.Options)) (*sns.GetTopicAttributesOutput, error) {
	if f.topicErr != nil {
		return nil, f.topicErr
	}
	return &sns.GetTopicAttributesOutput{}, nil
}

func (f *fakeSNS) Unsubscribe(context.Context, *sns.UnsubscribeInput, ...func(*sns.Options)) (*sns.UnsubscribeOutput, error) {
	return &sns.UnsubscribeOutput{}, nil
}

func lifecycledSub(subArn string) snstypes.Subscription {
	return snstypes.Subscription{
		Endpoint:        aws.String("arn:aws:sqs:us-east-1:123456789012:lifecycled-i-0abc"),
		TopicArn:        aws.String("arn:aws:sns:us-east-1:123456789012:asg-topic"),
		SubscriptionArn: aws.String(subArn),
	}
}

// A subscription is only queued for deletion when its topic is confirmed gone.
// A non-NotFound lookup failure must abort, never delete a live subscription.
func TestListInactiveSubscriptions(t *testing.T) {
	notFound := &smithy.GenericAPIError{Code: "NotFound", Message: "no such topic"}
	accessDenied := &smithy.GenericAPIError{Code: "AuthorizationError", Message: "no perms"}

	tests := []struct {
		name     string
		subs     []snstypes.Subscription
		topicErr error
		wantSubs []string
		wantErr  bool
	}{
		{
			name:     "missing topic queues the subscription",
			subs:     []snstypes.Subscription{lifecycledSub("sub-1")},
			topicErr: notFound,
			wantSubs: []string{"sub-1"},
		},
		{
			name:     "existing topic keeps the subscription",
			subs:     []snstypes.Subscription{lifecycledSub("sub-1")},
			wantSubs: nil,
		},
		{
			name:     "non-NotFound lookup failure aborts instead of deleting",
			subs:     []snstypes.Subscription{lifecycledSub("sub-1")},
			topicErr: accessDenied,
			wantErr:  true,
		},
		{
			name: "non-lifecycled endpoint is ignored",
			subs: []snstypes.Subscription{{
				Endpoint:        aws.String("https://example.com/hook"),
				TopicArn:        aws.String("arn:aws:sns:us-east-1:123456789012:other"),
				SubscriptionArn: aws.String("sub-1"),
			}},
			wantSubs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := listInactiveSubscriptions(context.Background(), &fakeSNS{subs: tt.subs, topicErr: tt.topicErr})

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.wantSubs) {
				t.Fatalf("subscriptions = %v, want %v", got, tt.wantSubs)
			}
			for i := range got {
				if got[i] != tt.wantSubs[i] {
					t.Errorf("subscriptions[%d] = %q, want %q", i, got[i], tt.wantSubs[i])
				}
			}
		})
	}
}

// Only queues that match the lifecycled-i- naming scheme and whose instance is
// no longer running are selected for deletion; everything else is left alone.
func TestFilterInactiveQueues(t *testing.T) {
	const (
		dead    = "https://sqs.us-east-1.amazonaws.com/123456789012/lifecycled-i-0dead"
		running = "https://sqs.us-east-1.amazonaws.com/123456789012/lifecycled-i-0run"
	)
	runningSet := map[string]struct{}{"i-0run": {}}

	tests := []struct {
		name string
		urls []string
		want []string
	}{
		{
			name: "queue for a terminated instance is inactive",
			urls: []string{dead},
			want: []string{dead},
		},
		{
			name: "queue for a running instance is kept",
			urls: []string{running},
			want: nil,
		},
		{
			name: "queue without the i- instance prefix is ignored",
			urls: []string{"https://sqs.us-east-1.amazonaws.com/123456789012/lifecycled-notaninstance"},
			want: nil,
		},
		{
			name: "non-lifecycled queue is ignored",
			urls: []string{"https://sqs.us-east-1.amazonaws.com/123456789012/some-other-queue"},
			want: nil,
		},
		{
			name: "mixed list keeps running, deletes terminated, skips unrelated",
			urls: []string{running, dead, "https://sqs.eu-west-2.amazonaws.com/111122223333/unrelated"},
			want: []string{dead},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterInactiveQueues(tt.urls, runningSet)
			if len(got) != len(tt.want) {
				t.Fatalf("inactive = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("inactive[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// deleteQueues tolerates QueueDoesNotExist but must surface any other delete
// error rather than reporting success. The propagation case is looped because
// the ordering it guards (publishing the error before signalling wg.Done) only
// fails on some interleavings, so a single pass can pass even when broken.
func TestDeleteQueues(t *testing.T) {
	boom := errors.New("delete failed")
	del := func(failures map[string]error) func(context.Context, string) error {
		return func(_ context.Context, queue string) error {
			return failures[queue]
		}
	}

	t.Run("all deletes succeed", func(t *testing.T) {
		count, err := deleteQueues(context.Background(), []string{"a", "b", "c"}, 3, del(nil))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if count != 3 {
			t.Errorf("count = %d, want 3", count)
		}
	})

	t.Run("QueueDoesNotExist is tolerated", func(t *testing.T) {
		fn := del(map[string]error{"b": &sqstypes.QueueDoesNotExist{}})
		if _, err := deleteQueues(context.Background(), []string{"a", "b", "c"}, 3, fn); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("a real delete error propagates", func(t *testing.T) {
		fn := del(map[string]error{"b": boom})
		for i := 0; i < 200; i++ {
			if _, err := deleteQueues(context.Background(), []string{"a", "b", "c", "d"}, 4, fn); !errors.Is(err, boom) {
				t.Fatalf("iteration %d: error = %v, want %v", i, err, boom)
			}
		}
	})
}
