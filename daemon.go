package lifecycled

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/sns"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/sirupsen/logrus"
)

// New creates a new lifecycle Daemon.
func New(config *Config, sess *session.Session, logger *logrus.Logger) *Daemon {
	return NewDaemon(
		config,
		sqs.New(sess),
		sns.New(sess),
		autoscaling.New(sess),
		ec2metadata.New(sess),
		logger,
	)
}

// NewDaemon creates a new Daemon.
func NewDaemon(
	config *Config,
	sqsClient SQSClient,
	snsClient SNSClient,
	asgClient AutoscalingClient,
	metadata *ec2metadata.EC2Metadata,
	logger *logrus.Logger,
) *Daemon {
	daemon := &Daemon{
		instanceID: config.InstanceID,
		logger:     logger,
	}
	if config.SpotListener {
		daemon.AddListener(NewSpotListener(config.InstanceID, metadata, config.SpotListenerInterval))
	}
	if config.SNSTopic != "" {
		queue := NewQueue(
			fmt.Sprintf("lifecycled-%s", config.InstanceID),
			config.SNSTopic,
			sqsClient,
			snsClient,
		)
		daemon.AddListener(NewAutoscalingListener(config.InstanceID, queue, asgClient))
	}
	return daemon
}

// Config for the Lifecycled Daemon.
type Config struct {
	InstanceID           string
	SNSTopic             string
	SpotListener         bool
	SpotListenerInterval time.Duration
}

// Daemon is what orchestrates the listening and execution of the handler on a termination notice.
type Daemon struct {
	instanceID string
	listeners  []Listener
	logger     *logrus.Logger
}

// Start the Daemon.
func (d *Daemon) Start(ctx context.Context) (notice TerminationNotice, err error) {
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

Listener:
	for {
		select {
		case <-listenerCtx.Done():
			// Make sure the underlying context was not cancelled
			if ctx.Err() != context.Canceled {
				err = errors.New("an error occured")
			}
			break Listener
		case n := <-notices:
			log.WithField("notice", n.Type()).Info("Received termination notice")
			notice = n
			break Listener
		}
	}
	return notice, err
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
