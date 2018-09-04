package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	log "github.com/Sirupsen/logrus"
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
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.NewTicker(time.Second * 5).C:
				timeout, hasTimeout, err := getTerminationTime(ctx, metadataURLTerminationTime)
				if err != nil {
					log.WithError(err).Info("Failed to get spot termination time")
					continue
				}
				if hasTimeout {
					ch <- timeout
				}
			}
		}
	}()

	return ch
}

// getTermination time returns a termination time, whether one exists and any error
func getTerminationTime(ctx context.Context, metadataURL string) (time.Time, bool, error) {
	req, err := http.NewRequest(http.MethodGet, metadataURL, nil)
	if err != nil {
		return time.Time{}, false, err
	}

	client := http.Client{
		Timeout: time.Second * 5,
	}
	res, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return time.Time{}, false, err
	}

	defer res.Body.Close()

	// will return 200 OK with termination notice
	if res.StatusCode != http.StatusOK {
		return time.Time{}, false, nil
	}

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return time.Time{}, true, fmt.Errorf("Failed to read response from metadata service: %v", err)
	}

	// if 200 OK, expect a body like 2015-01-05T18:02:00Z
	t, err := time.Parse(terminationTimeFormat, string(body))
	if err != nil {
		return time.Time{}, true, fmt.Errorf("Failed to parse time in termination notice: %v", err)
	}

	return t, true, nil
}
