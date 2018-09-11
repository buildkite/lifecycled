package main

import (
	"context"
	"os"
	"os/exec"
	"sync"

	log "github.com/sirupsen/logrus"
)

type Daemon struct {
	LifecycleMonitor *LifecycleMonitor
	SpotMonitor      *SpotMonitor

	InstanceID string
	Handler    *os.File
}

type TerminationNotice struct {
	Done chan struct{}
	Args []string
}

// Start the daemon.
func (d *Daemon) Start(ctx context.Context) error {
	var wg sync.WaitGroup

	var errCh = make(chan error, 2)
	var termCh = make(chan TerminationNotice)

	subctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Process lifecycle events
	if d.LifecycleMonitor != nil {
		wg.Add(1)

		go func() {
			defer wg.Done()
			if err := d.LifecycleMonitor.Run(subctx, termCh); err != nil {
				log.WithError(err).Debug("Lifecycle monitor failed with error")
				errCh <- err
				cancel()
			}
			log.Debug("Lifecycle monitor terminating")
		}()
	}

	// Process spot notifications
	if d.SpotMonitor != nil {
		wg.Add(1)

		go func() {
			defer wg.Done()
			if err := d.SpotMonitor.Run(subctx, termCh); err != nil {
				log.WithError(err).Debug("Spot monitor failed with error")
				errCh <- err
				cancel()
			}
			log.Debug("Spot monitor terminating")
		}()
	}

	// Handle termination notices
	go func() {
		for term := range termCh {
			log.Info("Received termination notice, executing handler")
			if err := executeHandler(subctx, d.Handler, term.Args); err != nil {
				log.WithError(err).Info("Handler finished with an error")
			} else {
				log.Info("Handler finished successfully")
				term.Done <- struct{}{}
				cancel()
			}
		}
	}()

	// Wait for services to finish via context and close our channels
	wg.Wait()
	log.Debug("Services finished, closing channels")
	close(errCh)
	close(termCh)

	// Return any errors, only the first matters
	for err := range errCh {
		return err
	}

	return nil
}

func executeHandler(ctx context.Context, command *os.File, args []string) error {
	cmd := exec.CommandContext(ctx, command.Name(), args...)
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
