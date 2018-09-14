package lifecycled_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/itsdalmo/lifecycled"
)

func newMetadataStub(instanceID, terminationTime string) *httptest.Server {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var resp string

		switch r.RequestURI {
		case "/latest/meta-data/instance-id":
			resp = instanceID
		case "/latest/meta-data/spot/termination-time":
			resp = terminationTime
		}

		if resp == "" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Write([]byte(resp))
	})
	return httptest.NewServer(handler)
}

func TestSpotListener(t *testing.T) {
	tests := []struct {
		description     string
		instanceID      string
		terminationTime string
		expectNotice    bool
		expectError     bool
		interrupt       bool
	}{
		{
			description:     "sends notice if a termination notice is found",
			instanceID:      "i-00000000000",
			terminationTime: "2006-01-02T15:04:05Z01:00",
			expectNotice:    true,
		},
		{
			description:     "can be interrupted by cancelling the context",
			instanceID:      "i-00000000000",
			terminationTime: "2006-01-02T15:04:05Z",
			expectNotice:    false,
			interrupt:       true,
		},
		{
			description:     "handles invalid time format",
			instanceID:      "i-00000000000",
			terminationTime: "invalidtimeformat",
			expectNotice:    false,
		},
		{
			description:  "exits with an error if metadata is not available",
			expectNotice: false,
			expectError:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			server := newMetadataStub(tc.instanceID, tc.terminationTime)
			defer server.Close()

			metadata := ec2metadata.New(session.New(), &aws.Config{
				Endpoint:   aws.String(server.URL + "/latest"),
				DisableSSL: aws.Bool(true),
			})

			// Record whether or not a notice was recieved
			var (
				receivedNotice bool
				wg             sync.WaitGroup
			)
			notices := make(chan lifecycled.TerminationNotice, 1)

			wg.Add(1)
			go func() {
				defer wg.Done()
				for range notices {
					receivedNotice = true
					break
				}
			}()

			ctx, cancel := context.WithCancel(context.TODO())
			defer cancel()
			if tc.interrupt {
				cancel()
			}

			listener := lifecycled.NewSpotListener(tc.instanceID, metadata)
			err := listener.Start(ctx, notices)

			if tc.expectError && err == nil {
				t.Errorf("expected an error to occur")
			}
			if !tc.expectError && err != nil {
				t.Errorf("unexpected error: %s", err)
			}
			close(notices)

			wg.Wait()
			if tc.expectNotice && !receivedNotice {
				t.Errorf("expected to receive a notice")
			}
		})
	}
}
