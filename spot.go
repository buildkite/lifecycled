package lifecycled

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/sirupsen/logrus"
)

// MetadataClient is the subset of the EC2 instance metadata API used by the daemon.
type MetadataClient interface {
	GetMetadata(context.Context, *imds.GetMetadataInput, ...func(*imds.Options)) (*imds.GetMetadataOutput, error)
}

// NewSpotListener ...
func NewSpotListener(instanceID string, metadata MetadataClient, interval time.Duration) *SpotListener {
	return &SpotListener{
		listenerType: "spot",
		instanceID:   instanceID,
		metadata:     metadata,
		interval:     interval,
	}
}

// SpotListener ...
type SpotListener struct {
	listenerType string
	instanceID   string
	metadata     MetadataClient
	interval     time.Duration
}

// Type returns a string describing the listener type.
func (l *SpotListener) Type() string {
	return l.listenerType
}

// Start the spot termination notice listener.
func (l *SpotListener) Start(ctx context.Context, notices chan<- TerminationNotice, log *logrus.Entry) error {
	// Probe the metadata service once so we fail fast when not on EC2.
	if _, err := l.metadataValue(ctx, "instance-id"); err != nil {
		return fmt.Errorf("ec2 metadata is not available: %w", err)
	}

	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			log.Debug("Polling ec2 metadata for spot termination notices")

			out, err := l.metadataValue(ctx, "spot/termination-time")
			if err != nil {
				// Shutting down: the next loop iteration returns via ctx.Done().
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					continue
				}
				// Metadata returns 404 when there is no termination notice available
				var statusErr interface{ HTTPStatusCode() int }
				if errors.As(err, &statusErr) && statusErr.HTTPStatusCode() == http.StatusNotFound {
					continue
				}
				log.WithError(err).Warn("Failed to get spot termination")
				continue
			}
			if out == "" {
				log.Error("Empty response from metadata")
				continue
			}
			t, err := time.Parse(time.RFC3339, out)
			if err != nil {
				log.WithError(err).Error("Failed to parse termination time")
				continue
			}
			notices <- &spotTerminationNotice{
				noticeType:      l.Type(),
				instanceID:      l.instanceID,
				transition:      "ec2:SPOT_INSTANCE_TERMINATION",
				terminationTime: t,
			}
			return nil
		}
	}
}

// metadataValue fetches a single instance metadata path and returns its value.
func (l *SpotListener) metadataValue(ctx context.Context, path string) (string, error) {
	out, err := l.metadata.GetMetadata(ctx, &imds.GetMetadataInput{Path: path})
	if err != nil {
		return "", err
	}
	defer func() { _ = out.Content.Close() }()
	b, err := io.ReadAll(out.Content)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

type spotTerminationNotice struct {
	noticeType      string
	instanceID      string
	transition      string
	terminationTime time.Time
}

func (n *spotTerminationNotice) Type() string {
	return n.noticeType
}

func (n *spotTerminationNotice) Handle(ctx context.Context, handler Handler, _ *logrus.Entry) error {
	return handler.Execute(ctx, n.transition, n.instanceID, n.terminationTime.Format(time.RFC3339))
}
