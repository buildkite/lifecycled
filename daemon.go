package lifecycled

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// Daemon is what orchestrates the listening and execution of the handler on a termination notice.
type Daemon struct {
	instanceID string
	handler    Handler
	listeners  []Listener
	logger     *logrus.Logger
}

// NewDaemon creates a new Daemon.
func NewDaemon(instanceID string, handler Handler, logger *logrus.Logger, listeners ...Listener) *Daemon {
	return &Daemon{
		instanceID: instanceID,
		handler:    handler,
		listeners:  listeners,
		logger:     logger,
	}
}

// Start the Daemon.
func (d *Daemon) Start(ctx context.Context) error {
	log := d.logger.WithField("instanceId", d.instanceID)

	// Use a buffered channel to avoid deadlocking a goroutine when we stop listening
	notices := make(chan TerminationNotice, len(d.listeners))
	defer close(notices)

	// Always wait for all listeners to exit before returning from this function
	var wg sync.WaitGroup
	defer wg.Wait()

	// Add a child context to stop all listeners when one has returned
	listenerCtx, stopListening := context.WithCancel(ctx)
	defer stopListening()

	for _, listener := range d.listeners {
		wg.Add(1)

		l := log.WithField("listener", listener.Type())

		go func() {
			defer wg.Done()

			if err := listener.Start(listenerCtx, notices, l); err != nil {
				l.WithError(err).Error("Failed to start listener")
				stopListening()
			} else {
				l.Info("Stopped listener")
			}
		}()
		l.Info("Starting listener")
	}

	log.Info("Waiting for termination notices")
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-listenerCtx.Done():
			return errors.New("an error occured")
		case n := <-notices:
			// Stop listeners immediately since we will not process any more notices
			stopListening()

			l := log.WithField("notice", n.Type())
			l.Info("Received termination notice: executing handler")

			start, err := time.Now(), n.Handle(ctx, d.handler, l)
			l = l.WithField("duration", time.Since(start).String())
			if err != nil {
				l.WithError(err).Error("Failed to execute handler")
			}
			l.Info("Handler finished succesfully")
			return nil
		}
	}
}

// AddListener to the Daemon.
func (d *Daemon) AddListener(l Listener) {
	d.listeners = append(d.listeners, l)
}

// Listener ...
type Listener interface {
	Type() string
	Start(context.Context, chan<- TerminationNotice, *logrus.Entry) error
}

// TerminationNotice ...
type TerminationNotice interface {
	Type() string
	Handle(context.Context, Handler, *logrus.Entry) error
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
