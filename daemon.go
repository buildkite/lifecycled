package main

import (
	"context"
	"sync"

	log "github.com/sirupsen/logrus"
)

type Daemon struct {
	LifecycleMonitor *LifecycleMonitor
	SpotMonitor      *SpotMonitor
}

// Start the daemon.
func (d *Daemon) Start(ctx context.Context) error {
	var wg sync.WaitGroup
	var errCh = make(chan error)

	// Process lifecycle events
	if d.LifecycleMonitor != nil {
		wg.Add(1)

		go func() {
			defer wg.Done()
			errCh <- d.LifecycleMonitor.Run(ctx)
		}()
	}

	// Process spot notifications
	if d.SpotMonitor != nil {
		wg.Add(1)

		go func() {
			defer wg.Done()
			errCh <- d.SpotMonitor.Run(ctx)
		}()
	}

	// Wait for monitors to have finished and then close the error channel
	go func() {
		wg.Wait()
		close(errCh)
	}()

	var errs []error

	// Wait for either an error or nil back from each monitor. Blocks until
	// the above wait fires
	for err := range errCh {
		log.WithFields(log.Fields{"err": err}).Debugf("Monitor finished")

		if err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return errs[0]
	}

	return nil
}
