package main

import (
	"context"
	"io/ioutil"
	"net/http"
	"os"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	metadataURLTerminationTime = "http://169.254.169.254/latest/meta-data/spot/termination-time"
	terminationTransition      = "ec2:SPOT_INSTANCE_TERMINATION"
	terminationTimeFormat      = "2006-01-02T15:04:05Z"
)

type SpotMonitor struct {
	InstanceID string
	Handler    *os.File
}

func (s *SpotMonitor) Run(ctx context.Context, termCh chan TerminationNotice) error {
	log.Debugf("Polling metadata service for spot termination notices")

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.NewTicker(time.Second * 5).C:
			has, err := hasSpotTerminationNotice()
			if err != nil {
				log.WithError(err).Info("Failed to query spot termination notice")
				continue
			}
			if has {
				log.Info("Received spot termination notice")
				doneCh := make(chan struct{})
				errCh := make(chan error)

				termCh <- TerminationNotice{
					Done:  doneCh,
					Error: errCh,
					Args:  []string{terminationTransition, s.InstanceID},
				}

				select {
				case <-doneCh:
				case <-errCh:
				}
			}
		}
	}
}

func hasSpotTerminationNotice() (bool, error) {
	res, err := http.Get(metadataURLTerminationTime)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()

	// will return 200 OK with termination notice
	if res.StatusCode != http.StatusOK {
		return false, nil
	}

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return false, err
	}

	// if 200 OK, expect a body like 2015-01-05T18:02:00Z
	t, err := time.Parse(terminationTimeFormat, string(body))
	if err != nil {
		return false, err
	}

	log.WithFields(log.Fields{
		"time": t,
	}).Info("Received spot instance termination notice")

	return true, nil
}
