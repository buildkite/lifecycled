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
func (d *Daemon) Start(userCtx context.Context) error {
	// Add a child context to stop all listeners when one has returned
	ctx, stop := context.WithCancel(userCtx)
	defer stop()

	// Use a buffered channel to avoid deadlocking a goroutine when we stop listening
	notices := make(chan Notice, 1)

	var wg sync.WaitGroup
	for _, listener := range d.listeners {
		wg.Add(1)

		go func() {
			defer wg.Done()
			defer stop()

			if err := listener.Start(ctx, notices); err != nil {
				log.WithError(err).Errorf("Failed to start listening for %s notices", listener.Type())
			} else {
				log.Infof("Stopped listening for %s termination notices", listener.Type())
			}
		}()
		log.Infof("Listening for %s termination notices", listener.Type())
	}

	go func() {
		wg.Wait()
		close(notices)
	}()

	for n := range notices {
		log.Infof("Received a %s termination notice: executing handler", n.Type())

		start, err := time.Now(), n.Handle(userCtx, d.handler)
		if err != nil {
			log.WithField("duration", time.Since(start)).WithError(err).Error("Failed to execute handler")
		}
		log.WithField("duration", time.Since(start)).Error("Handler executed succesfully")
		return nil
	}

	// We should only reach this code if a notice was not received,
	// which means we are either exiting because of an error or because
	// the user context was interrupted (by e.g. SIGINT or SIGTERM).
	if userCtx.Err() != context.Canceled {
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
	Start(context.Context, chan<- Notice) error
}

// Notice ...
type Notice interface {
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
