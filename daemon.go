package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// Daemon is what orchestrates the listening and execution of the handler on a termination notice.
type Daemon struct {
	instanceID string
	handler    Handler
	listeners  []Listener
}

// NewDaemon creates a new Daemon.
func NewDaemon(instanceID string, handler Handler, listeners ...Listener) *Daemon {
	return &Daemon{
		instanceID: instanceID,
		handler:    handler,
		listeners:  listeners,
	}
}

// Start the Daemon.
func (d *Daemon) Start(ctx context.Context) error {
	// Add a child context to stop all listeners when one has returned
	listenerCtx, stopListening := context.WithCancel(ctx)
	defer stopListening()

	// Use a buffered channel to avoid deadlocking a goroutine when we stop listening
	notices := make(chan TerminationNotice, 1)

	var wg sync.WaitGroup
	for _, listener := range d.listeners {
		wg.Add(1)

		l := log.WithField("listener", listener.Type())

		go func() {
			defer wg.Done()
			defer stopListening()

			if err := listener.Start(listenerCtx, notices); err != nil {
				l.WithError(err).Error("Failed to start listener")
			} else {
				l.Info("Stopped listener")
			}
		}()
		l.Info("Starting listener")
	}

	go func() {
		wg.Wait()
		close(notices)
	}()

	log.Info("Waiting for termination notices")
	for n := range notices {
		l := log.WithField("notice", n.Type())
		l.Info("Received termination notice: executing handler")

		start, err := time.Now(), n.Handle(ctx, d.handler)
		l = l.WithField("duration", time.Since(start).String())
		if err != nil {
			l.WithError(err).Error("Failed to execute handler")
		}
		l.Info("Handler finished succesfully")
		return nil
	}

	// We should only reach this code if a notice was not received,
	// which means we are either exiting because of an error or because
	// the user context was interrupted (by e.g. SIGINT or SIGTERM).
	if ctx.Err() == context.Canceled {
		return nil
	}
	return errors.New("an error occured")
}

// AddListener to the Daemon.
func (d *Daemon) AddListener(l Listener) {
	d.listeners = append(d.listeners, l)
}

// Listener ...
type Listener interface {
	Type() string
	Start(context.Context, chan<- TerminationNotice) error
}

// TerminationNotice ...
type TerminationNotice interface {
	Type() string
	Handle(context.Context, Handler) error
}

// Handler ...
type Handler interface {
	Execute(ctx context.Context, instanceID, transition string) error
}

// NewFileHandler ...
func NewFileHandler(file *os.File) *FileHandler {
	return &FileHandler{file: file}
}

// FileHandler ...
type FileHandler struct {
	file *os.File
}

// Execute the file handler.
func (h *FileHandler) Execute(ctx context.Context, instanceID, transition string) error {
	cmd := exec.CommandContext(ctx, h.file.Name(), instanceID, transition)
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
