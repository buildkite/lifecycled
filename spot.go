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
	terminationTransition      = "ec2:SPOT_INSTANCE_TERMINATION"
	terminationTimeFormat      = "2006-01-02T15:04:05Z"
)

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
