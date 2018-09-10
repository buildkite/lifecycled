package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	metadataURLTerminationTime = "http://169.254.169.254/latest/meta-data/spot/termination-time"
	terminationTimeFormat      = "2006-01-02T15:04:05Z"
)

// NewSpotListener ...
func NewSpotListener(instanceID string) *SpotListener {
	return &SpotListener{
		listenerType: "spot",
		instanceID:   instanceID,
	}
}

// SpotListener ...
type SpotListener struct {
	listenerType string
	instanceID   string
}

// Type returns a string describing the listener type.
func (l *SpotListener) Type() string {
	return l.listenerType
}

// Start the spot termination notice listener.
func (l *SpotListener) Start(ctx context.Context, notices chan<- Notice) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.NewTicker(time.Second * 5).C:
			t, err := getSpotTermination(ctx)
			if err != nil {
				log.WithError(err).Error("Failed to get spot termination")
			}
			if t == nil {
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

func getSpotTermination(ctx context.Context) (*time.Time, error) {
	log.Debugf("Polling ec2 metadata for spot termination notices")
	res, err := http.Get(metadataURLTerminationTime)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	// will return 200 OK with termination notice
	if res.StatusCode != http.StatusOK {
		return nil, nil
	}

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	// if 200 OK, expect a body like 2015-01-05T18:02:00Z
	t, err := time.Parse(terminationTimeFormat, string(body))
	if err != nil {
		return nil, fmt.Errorf("failed to parse time in termination notice: %s", err)
	}
	return &t, err
}

type spotNotice struct {
	noticeType      string
	instanceID      string
	transition      string
	terminationTime *time.Time
}

func (n *spotNotice) Type() string {
	return n.noticeType
}

func (n *spotNotice) Handle(ctx context.Context, handler Handler) error {
	return handler.Execute(ctx, n.instanceID, n.transition)
}
