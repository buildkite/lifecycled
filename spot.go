package main

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	log "github.com/sirupsen/logrus"
)

const (
	terminationTransition = "ec2:SPOT_INSTANCE_TERMINATION"
	terminationTimeFormat = "2006-01-02T15:04:05Z"
)

func pollSpotTermination(ctx context.Context, svc *ec2metadata.EC2Metadata) chan time.Time {
	ch := make(chan time.Time)

	log.Debugf("Polling metadata service for spot termination notices")

	go func() {
		// Close channel before returning since this (goroutine) is the sending side.
		defer close(ch)

		for {
			select {
			case <-ctx.Done():
				return

			case <-time.NewTicker(time.Second * 5).C:
				resp, err := svc.GetMetadata("spot/termination-time")
				if err != nil {
					log.WithError(err).Info("Failed to fetch spot termination time")
					continue
				}

				// expect a body like 2015-01-05T18:02:00Z
				t, err := time.Parse(terminationTimeFormat, resp)
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
