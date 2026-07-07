package lifecycled

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
)

type stubMetadataClient struct {
	out *imds.GetMetadataOutput
	err error
}

func (s *stubMetadataClient) GetMetadata(_ context.Context, _ *imds.GetMetadataInput, _ ...func(*imds.Options)) (*imds.GetMetadataOutput, error) {
	return s.out, s.err
}

// Start probes the metadata service first so it fails fast off EC2 rather than
// polling a service that isn't there.
func TestSpotListenerStartProbeFailure(t *testing.T) {
	metadata := &stubMetadataClient{err: errors.New("connection refused")}
	listener := NewSpotListener("i-1234567890", metadata, time.Millisecond)

	logger := logrus.New()
	logger.SetOutput(io.Discard)
	notices := make(chan TerminationNotice, 1)

	// Bound the call so a regression that drops the probe fails the test instead
	// of polling until the deadline and hanging.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := listener.Start(ctx, notices, logrus.NewEntry(logger))
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "ec2 metadata is not available") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "ec2 metadata is not available")
	}
}

type metadataResponse struct {
	status int
	body   string
}

// newSpotMetadataServer is an IMDS stub that passes the IMDSv2 token handshake
// and instance-id probe, returns badResp on the first spot/termination-time poll,
// then goodTime on later polls so the listener emits a notice and Start returns
// on its own instead of being cancelled mid-poll.
func newSpotMetadataServer(instanceID, goodTime string, badResp metadataResponse) *httptest.Server {
	var termHits int64
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.RequestURI == "/latest/api/token" {
			_, _ = w.Write([]byte("token"))
			return
		}
		switch r.RequestURI {
		case "/latest/meta-data/instance-id":
			_, _ = w.Write([]byte(instanceID))
		case "/latest/meta-data/spot/termination-time":
			if atomic.AddInt64(&termHits, 1) == 1 {
				if badResp.status != http.StatusOK {
					http.Error(w, badResp.body, badResp.status)
					return
				}
				_, _ = w.Write([]byte(badResp.body))
				return
			}
			_, _ = w.Write([]byte(goodTime))
		default:
			http.Error(w, "404 - not found", http.StatusNotFound)
		}
	}))
}

// The polling loop must skip a 404 silently and log-and-skip empty or
// unparseable bodies, continuing in every case. The 404 case drives a real IMDS
// 404 through the SDK so a future change to how IMDS wraps status errors (which
// would break the errors.As detection) fails this test.
func TestSpotListenerPollingBranches(t *testing.T) {
	const (
		instanceID = "i-1234567890"
		goodTime   = "2026-06-29T12:00:00Z"
	)

	tests := []struct {
		name string
		bad  metadataResponse
		// wantLog, if set, must appear in the logs; the 404 branch instead must
		// not log the generic warning, asserted via wantNoTerminationWarn.
		wantLog               string
		wantNoTerminationWarn bool
	}{
		{
			name:                  "404 is skipped without a warning",
			bad:                   metadataResponse{status: http.StatusNotFound, body: "no termination time"},
			wantNoTerminationWarn: true,
		},
		{
			name:    "empty body is logged and skipped",
			bad:     metadataResponse{status: http.StatusOK, body: ""},
			wantLog: "Empty response from metadata",
		},
		{
			name:    "unparseable body is logged and skipped",
			bad:     metadataResponse{status: http.StatusOK, body: "not-a-timestamp"},
			wantLog: "Failed to parse termination time",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := newSpotMetadataServer(instanceID, goodTime, tc.bad)
			defer server.Close()

			metadata := imds.New(imds.Options{Endpoint: server.URL})
			listener := NewSpotListener(instanceID, metadata, time.Millisecond)

			logger, hook := logrustest.NewNullLogger()
			notices := make(chan TerminationNotice, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			// Start returns once a later poll yields a valid time and a notice is
			// emitted, proving the branch under test was skipped, not fatal.
			if err := listener.Start(ctx, notices, logrus.NewEntry(logger)); err != nil {
				t.Fatalf("Start returned error: %v", err)
			}

			select {
			case n := <-notices:
				if got := n.Type(); got != "spot" {
					t.Errorf("notice type = %q, want %q", got, "spot")
				}
			default:
				t.Error("expected a termination notice after the listener recovered, got none")
			}

			entries := hook.AllEntries()
			if tc.wantLog != "" && !logged(entries, tc.wantLog) {
				t.Errorf("expected a log entry containing %q, got %v", tc.wantLog, messages(entries))
			}
			if tc.wantNoTerminationWarn && logged(entries, "Failed to get spot termination") {
				t.Error("a 404 should be skipped silently, but the termination warning was logged; IMDS 404 detection may have regressed")
			}
		})
	}
}

func logged(entries []*logrus.Entry, substr string) bool {
	for _, e := range entries {
		if strings.Contains(e.Message, substr) {
			return true
		}
	}
	return false
}

func messages(entries []*logrus.Entry) []string {
	msgs := make([]string, 0, len(entries))
	for _, e := range entries {
		msgs = append(msgs, e.Message)
	}
	return msgs
}
