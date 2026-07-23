package lifecycled_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/buildkite/lifecycled"
	"github.com/buildkite/lifecycled/mocks"
	logrus "github.com/sirupsen/logrus/hooks/test"
	"go.uber.org/mock/gomock"
)

func newMetadataStub(instanceID, terminationTime string) *httptest.Server {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// IMDSv2 token negotiation; the value is not validated by the client.
		if r.Method == http.MethodPut && r.RequestURI == "/latest/api/token" {
			_, _ = w.Write([]byte("token"))
			return
		}

		var resp string
		switch r.RequestURI {
		case "/latest/meta-data/instance-id":
			resp = instanceID
		case "/latest/meta-data/spot/termination-time":
			resp = terminationTime
		}

		if resp == "" {
			http.Error(w, "404 - not found", http.StatusNotFound)
			return
		}
		if _, err := w.Write([]byte(resp)); err != nil {
			http.Error(w, "500 - internal server error", http.StatusInternalServerError)
			return
		}
	})
	return httptest.NewServer(handler)
}

func newSQSMessage(instanceID string) sqstypes.Message {
	m := fmt.Sprintf(`
{
	"Time": "2016-02-26T21:09:59.517Z",
	"AutoScalingGroupName": "group",
	"EC2InstanceId": "%s",
	"LifecycleActionToken": "token",
	"LifecycleTransition": "autoscaling:EC2_INSTANCE_TERMINATING",
	"LifecycleHookName": "hook"
}
	`, instanceID)

	e, err := json.Marshal(&lifecycled.Envelope{
		Type:    "type",
		Subject: "subject",
		Time:    time.Now(),
		Message: m,
	})

	if err != nil {
		panic(err)
	}

	return sqstypes.Message{
		Body:          aws.String(string(e)),
		ReceiptHandle: aws.String("handle"),
	}
}

func TestDaemon(t *testing.T) {
	var (
		instanceID          = "i-000000000000"
		spotTerminationTime = "2006-01-02T15:04:05+02:00"
	)

	tests := []struct {
		description        string
		snsTopic           string
		tags               string
		spotListener       bool
		subscribeError     error
		expectedNoticeType string
		expectDaemonError  bool
	}{
		{
			description:        "works with autoscaling listener",
			snsTopic:           "topic",
			expectedNoticeType: "autoscaling",
		},
		{
			description:        "works with spot termination listener",
			spotListener:       true,
			expectedNoticeType: "spot",
		},
		{
			description:       "cleans up queue if sns topic does not exist",
			snsTopic:          "invalid",
			subscribeError:    errors.New("invalid topic"),
			expectDaemonError: true,
		},
		{
			description:        "works with empty tags",
			snsTopic:           "topic",
			tags:               "",
			expectedNoticeType: "autoscaling",
		},
		{
			description:        "works with two tags",
			snsTopic:           "topic",
			tags:               "Environment=production,Team=platform",
			expectedNoticeType: "autoscaling",
		},
		{
			description:        "spot listener ignores tags",
			spotListener:       true,
			tags:               "Environment=production",
			expectedNoticeType: "spot",
		},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			// Mock AWS SDK services
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			sq := mocks.NewMockSQSClient(ctrl)
			sn := mocks.NewMockSNSClient(ctrl)
			as := mocks.NewMockAutoscalingClient(ctrl)

			expectedTags := parseTagString(tc.tags)

			// Expected SQS calls
			if tc.snsTopic != "" {
				sq.EXPECT().CreateQueue(gomock.Any(), gomock.Any()).Times(1).DoAndReturn(
					func(_ context.Context, input *sqs.CreateQueueInput, _ ...func(*sqs.Options)) (*sqs.CreateQueueOutput, error) {
						if tc.tags != "" {
							if len(input.Tags) == 0 {
								t.Error("expected tags to be set in CreateQueue request but got none")
							} else {
								for key, expectedValue := range expectedTags {
									actualValue, ok := input.Tags[key]
									if !ok {
										t.Errorf("expected tag key '%s' not found in CreateQueue request", key)
									} else if actualValue != expectedValue {
										t.Errorf("tag '%s': expected value '%s' but got '%s'", key, expectedValue, actualValue)
									}
								}

								// Verify no extra tags were added
								if len(input.Tags) != len(expectedTags) {
									t.Errorf("expected %d tags but got %d tags", len(expectedTags), len(input.Tags))
								}
							}
						}
						return &sqs.CreateQueueOutput{
							QueueUrl: aws.String("url"),
						}, nil
					},
				)
				sq.EXPECT().GetQueueAttributes(gomock.Any(), gomock.Any()).Times(1).Return(&sqs.GetQueueAttributesOutput{
					Attributes: map[string]string{"QueueArn": "arn"},
				}, nil)
				sq.EXPECT().DeleteQueue(gomock.Any(), gomock.Any()).Times(1).Return(nil, nil)

				if tc.subscribeError == nil {
					sq.EXPECT().ReceiveMessage(gomock.Any(), gomock.Any()).MinTimes(1).Return(&sqs.ReceiveMessageOutput{
						Messages: []sqstypes.Message{newSQSMessage(instanceID)},
					}, nil)
					sq.EXPECT().DeleteMessage(gomock.Any(), gomock.Any()).MinTimes(1).Return(nil, nil)
				}
			}

			// Expected SNS calls
			if tc.snsTopic != "" {
				sn.EXPECT().Subscribe(gomock.Any(), gomock.Any()).Times(1).Return(&sns.SubscribeOutput{
					SubscriptionArn: aws.String("arn"),
				}, tc.subscribeError)

				if tc.subscribeError == nil {
					sn.EXPECT().Unsubscribe(gomock.Any(), gomock.Any()).Times(1).Return(nil, nil)
				}
			}

			// Stub the metadata endpoint
			server := newMetadataStub(instanceID, spotTerminationTime)
			defer server.Close()

			metadata := imds.New(imds.Options{Endpoint: server.URL})

			// Create and start the daemon
			logger, hook := logrus.NewNullLogger()
			ctx, cancel := context.WithTimeout(context.TODO(), 3*time.Second)
			defer cancel()

			config := &lifecycled.Config{
				InstanceID:           instanceID,
				SNSTopic:             tc.snsTopic,
				Tags:                 tc.tags,
				SpotListener:         tc.spotListener,
				SpotListenerInterval: 1 * time.Millisecond,
			}

			daemon := lifecycled.NewDaemon(config, sq, sn, as, metadata, logger)
			notice, err := daemon.Start(ctx)

			if err != nil {
				if !tc.expectDaemonError {
					// Include log entries (that are unique)
					logs := make(map[string]string)
					for _, e := range hook.AllEntries() {
						if _, ok := logs[e.Message]; !ok {
							logs[e.Message] = e.Level.String()
						}
					}
					var messages strings.Builder
					for k, v := range logs {
						if _, err := fmt.Fprintf(&messages, "%s - %s\n", v, k); err != nil {
							t.Errorf("unable to write log entry: %v\n", err)
						}
					}
					t.Errorf("unexpected error occured: %s: unique logs entries:\n%s", err, messages.String())
				}
			} else {
				if tc.expectDaemonError {
					t.Error("expected an error to occur")
				}
			}

			if tc.expectedNoticeType != "" {
				if notice == nil {
					t.Error("expected a notice to be returned")
				} else {
					if got, want := notice.Type(), tc.expectedNoticeType; got != want {
						t.Errorf("expected '%s' notice and got '%s'", want, got)
					}
				}
			}
		})
	}

}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name     string
		config   lifecycled.Config
		wantErrs []string
	}{
		{
			name:   "spot listener with a valid interval",
			config: lifecycled.Config{SpotListener: true, SpotListenerInterval: time.Second},
		},
		{
			name:   "autoscaling listener with a valid interval",
			config: lifecycled.Config{SNSTopic: "topic", AutoscalingHeartbeatInterval: time.Second},
		},
		{
			name:     "no listeners enabled",
			config:   lifecycled.Config{},
			wantErrs: []string{"no listeners enabled"},
		},
		{
			name:     "spot interval not positive",
			config:   lifecycled.Config{SpotListener: true, SpotListenerInterval: 0},
			wantErrs: []string{"spot-listener-interval"},
		},
		{
			name:     "spot interval negative",
			config:   lifecycled.Config{SpotListener: true, SpotListenerInterval: -time.Second},
			wantErrs: []string{"spot-listener-interval"},
		},
		{
			name:     "heartbeat interval not positive",
			config:   lifecycled.Config{SNSTopic: "topic", AutoscalingHeartbeatInterval: -time.Second},
			wantErrs: []string{"autoscaling-heartbeat-interval"},
		},
		{
			// Both listeners misconfigured: Validate reports both, not just the first.
			name:     "both intervals not positive",
			config:   lifecycled.Config{SpotListener: true, SpotListenerInterval: 0, SNSTopic: "topic", AutoscalingHeartbeatInterval: 0},
			wantErrs: []string{"spot-listener-interval", "autoscaling-heartbeat-interval"},
		},
		{
			// A disabled listener's interval is irrelevant and must not fail validation.
			name:   "disabled spot listener ignores its interval",
			config: lifecycled.Config{SNSTopic: "topic", AutoscalingHeartbeatInterval: time.Second, SpotListenerInterval: 0},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.config.Validate()
			if len(tc.wantErrs) == 0 {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want an error containing %q", tc.wantErrs)
			}
			for _, want := range tc.wantErrs {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Validate() error = %q, want it to contain %q", err.Error(), want)
				}
			}
		})
	}
}

func parseTagString(tagString string) map[string]string {
	tags := make(map[string]string)

	if tagString == "" {
		return tags
	}

	pairs := strings.Split(tagString, ",")
	for _, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}

		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			tags[key] = value
		}
	}

	return tags
}
