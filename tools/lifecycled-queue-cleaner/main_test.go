package main

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
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
