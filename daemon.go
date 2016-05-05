package lifecycled

import (
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/apex/log"
	"github.com/aws/aws-sdk-go/service/autoscaling"
)

const (
	heartbeatFrequency = time.Second * 10
)

type Daemon struct {
	Queue       Queue
	ReceiveOpts ReceiveOpts
	AutoScaling *autoscaling.AutoScaling
	Handler     *os.File
	Signals     chan os.Signal
}

func (d *Daemon) Start() error {
	log.Info("Starting lifecycled daemon")

	ch := make(chan Message)
	go func() {
		for m := range ch {
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
				"handler":    d.Handler,
			})

			handlerCtx.Info("Executing handler")
			timer := time.Now()

			code, err := executeHandler(d.Handler, []string{m.Transition, m.InstanceID}, d.Signals)
			executeCtx := handlerCtx.WithFields(log.Fields{
				"exitcode": code,
				"duration": time.Now().Sub(timer),
			})
			if err != nil {
				executeCtx.WithError(err).Error("Handler script failed")

				if err = d.Queue.Release(m); err != nil {
					handlerCtx.WithError(err).Error("Failed to release message to queue")
				} else {
					handlerCtx.Debug("Released message to queue")
				}
			} else {
				executeCtx.Info("Handler finished successfully")

				if err = d.Queue.Delete(m); err != nil {
					handlerCtx.WithError(err).Error("Failed to delete message from queue")
				} else {
					handlerCtx.Debug("Deleted message from queue")
				}
			}
		}
	}()

	return d.Queue.Receive(ch, d.ReceiveOpts)
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
		if err != nil {
			return syscall.WaitStatus(127), err
		}
		if exitError, ok := err.(*exec.ExitError); ok {
			return exitError.Sys().(syscall.WaitStatus), nil
		}
	}

	return syscall.WaitStatus(0), nil
}
