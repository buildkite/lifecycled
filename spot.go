package main

import (
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

func pollSpotTermination() chan time.Time {
	ch := make(chan time.Time)

	log.Debugf("Polling metadata service for spot termination notices")
	go func() {
		for range time.NewTicker(time.Second * 5).C {
			res, err := http.Get(metadataURLTerminationTime)
			if err != nil {
				log.WithError(err).Info("Failed to query metadata service")
				continue
			}
			defer res.Body.Close()

			// will return 200 OK with termination notice
			if res.StatusCode != http.StatusOK {
				continue
			}

			body, err := ioutil.ReadAll(res.Body)
			if err != nil {
				log.WithError(err).Info("Failed to read response from metadata service")
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
	}()

	return ch
}
