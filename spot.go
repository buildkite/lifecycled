package main

import (
	"context"
	"io/ioutil"
	"net/http"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	metadataURLTerminationTime = "http://169.254.169.254/latest/meta-data/spot/termination-time"
	terminationTransition      = "ec2:SPOT_INSTANCE_TERMINATION"
	terminationTimeFormat      = "2006-01-02T15:04:05Z"
)

func pollSpotTermination(ctx context.Context) chan time.Time {
	ch := make(chan time.Time)

	log.Debugf("Polling metadata service for spot termination notices")

	go func() {
		// Close channel before returning since this (goroutine) is the sending side.
		defer close(ch)
		retry := time.NewTicker(time.Second * 5).C
	Loop:
		for {
			select {
			case <-ctx.Done():
				break Loop
			case <-retry:
				res, err := http.Get(metadataURLTerminationTime)
				if err != nil {
					log.WithError(err).Info("Failed to query metadata service")
					continue
				}

				// We read the body immediately so that we can close the body in one place
				// and still use 'continue' if any of our conditions are false.
				body, err := ioutil.ReadAll(res.Body)
				res.Body.Close()
				if err != nil {
					log.WithError(err).Info("Failed to read response from metadata service")
					continue
				}

				// will return 200 OK with termination notice
				if res.StatusCode != http.StatusOK {
					continue
				}

				// if 200 OK, expect a body like 2015-01-05T18:02:00Z
				t, err := time.Parse(terminationTimeFormat, string(body))
				if err != nil {
					log.WithError(err).Info("Failed to parse time in termination notice")
					continue
				}

				ch <- t
			}
		}
	}()

	return ch
}
