package main

import (
	"context"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go/aws/ec2metadata"

	log "github.com/sirupsen/logrus"
)

const (
	metadataURLTerminationTime = "http://169.254.169.254/latest/meta-data/spot/termination-time"
	terminationTimeFormat      = "2006-01-02T15:04:05Z"
)

// NewSpotListener ...
func NewSpotListener(instanceID string, metadata *ec2metadata.EC2Metadata) *SpotListener {
	return &SpotListener{
		listenerType: "spot",
		instanceID:   instanceID,
		metadata:     metadata,
	}
}

// SpotListener ...
type SpotListener struct {
	listenerType string
	instanceID   string
	metadata     *ec2metadata.EC2Metadata
}

// Type returns a string describing the listener type.
func (l *SpotListener) Type() string {
	return l.listenerType
}

// Start the spot termination notice listener.
func (l *SpotListener) Start(ctx context.Context, notices chan<- Notice) error {
	if !l.metadata.Available() {
		return errors.New("ec2 metadata is not available")
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.NewTicker(time.Second * 5).C:
			log.Debugf("Polling ec2 metadata for spot termination notices")
			out, err := l.metadata.GetMetadata("spot/termination-time")
			if err != nil {
				log.WithError(err).Error("Failed to get spot termination")
				continue
			}
			if out == "" {
				log.WithError(err).Error("Empty response from metadata")
				continue
			}
			t, err := time.Parse(terminationTimeFormat, out)
			if out == "" {
				log.WithError(err).Error("Failed to parse termination time")
				continue
			}
			notices <- &spotNotice{
				noticeType:      l.Type(),
				instanceID:      l.instanceID,
				transition:      "ec2:SPOT_INSTANCE_TERMINATION",
				terminationTime: t,
			}
			return nil
		}
	}
}

type spotNotice struct {
	noticeType      string
	instanceID      string
	transition      string
	terminationTime time.Time
}

func (n *spotNotice) Type() string {
	return n.noticeType
}

func (n *spotNotice) Handle(ctx context.Context, handler Handler) error {
	return handler.Execute(ctx, n.instanceID, n.transition)
}
