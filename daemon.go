package lifecycled

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/aws/aws-sdk-go/service/autoscaling"
)

const (
	heartbeatFrequency = time.Second * 10
)

const (
	MatchAnyInstanceID = "*"
)

type Daemon struct {
	Queue       Queue
	ReceiveOpts ReceiveOpts
	AutoScaling *autoscaling.AutoScaling
	Handler     *os.File
	Signals     chan os.Signal

	// filters
	InstanceID string
}

func (d *Daemon) Start() error {
	if d.InstanceID == "" {
		return errors.New("Expected an instance id to filter")
	}

	log.Info("Starting lifecycled daemon")

	ch := make(chan Message)
	go func() {
		for m := range ch {
			if m.Transition == "" {
				continue
			}
			if d.InstanceID != MatchAnyInstanceID && d.InstanceID != m.InstanceID {
				log.WithFields(log.Fields{"instanceid": m.InstanceID}).Debug("Skipping filtered message")
				continue
			}
			d.handleMessage(m)
		}
	}()

	log.WithFields(log.Fields{"queue": d.Queue}).Info("Listening for events")
	return d.Queue.Receive(ch, d.ReceiveOpts)
}

func (d *Daemon) handleMessage(m Message) {
	ctx := log.WithFields(log.Fields{
		"transition": m.Transition,
		"instanceid": m.InstanceID,
	})

	ctx.Info("Received message")

	hbt := time.NewTicker(heartbeatFrequency)
	go func() {
		for _ = range hbt.C {
			ctx.Debug("Sending heartbeat")
			if err := sendHeartbeat(d.AutoScaling, m); err != nil {
				ctx.WithError(err).Error("Heartbeat failed")
			}
		}
	}()

	handlerCtx := log.WithFields(log.Fields{
		"transition": m.Transition,
		"instanceid": m.InstanceID,
		"handler":    d.Handler.Name(),
	})

	handlerCtx.Info("Executing handler")
	timer := time.Now()

	code, err := executeHandler(d.Handler, []string{m.Transition, m.InstanceID}, d.Signals)
	executeCtx := handlerCtx.WithFields(log.Fields{
		"exitcode": code,
		"duration": time.Now().Sub(timer),
	})
	hbt.Stop()

	if err != nil {
		executeCtx.WithError(err).Error("Handler script failed")
		if err := d.Queue.Release(m); err != nil {
			executeCtx.WithError(err).Error("Failed to release message back to queue")
			return
		}

		handlerCtx.Debug("Released message to queue")
		return
	}

	handlerCtx.Info("Handler finished successfully")

	if err = completeLifecycle(d.AutoScaling, m); err != nil {
		ctx.WithError(err).Error("Failed to complete lifecycle action")
		return
	}

	ctx.Info("Lifecycle action completed successfully")

	if err = d.Queue.Delete(m); err != nil {
		handlerCtx.WithError(err).Error("Failed to delete message from queue")
		return
	}

	handlerCtx.Debug("Deleted message from queue")
}

func executeHandler(command *os.File, args []string, sigs chan os.Signal) (syscall.WaitStatus, error) {
	cmd := exec.Command(command.Name(), args...)
	cmd.Env = os.Environ()
	// cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	go func() {
		sig := <-sigs
		if cmd.Process != nil {
			cmd.Process.Signal(sig)
		}
	}()

	if err := cmd.Run(); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			return exitError.Sys().(syscall.WaitStatus), nil
		}
	}

	return syscall.WaitStatus(0), nil
}
