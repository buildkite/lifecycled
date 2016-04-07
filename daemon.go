package lifecycled

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/apex/log"
)
import "github.com/aws/aws-sdk-go/service/autoscaling"

const (
	heartbeatFrequency = time.Second * 10
)

type Daemon struct {
	Queue       Queue
	ReceiveOpts ReceiveOpts
	AutoScaling *autoscaling.AutoScaling
	HooksDir    string
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

			hook, err := findHook(m, d.HooksDir)
			if err != nil {
				ctx.Error(err.Error())
				continue
			}

			hbt := time.NewTicker(heartbeatFrequency)
			go func() {
				for _ = range hbt.C {
					ctx.Debug("Heartbeat fired")
					if err := sendHeartbeat(d.AutoScaling, m); err != nil {
						ctx.Error(err.Error())
					}
				}
			}()

			hookCtx := log.WithFields(log.Fields{
				"transition": m.Transition,
				"instanceid": m.InstanceID,
				"hook":       hook,
			})

			hookCtx.Info("Executing hook")
			code, err := executeHook(hook, []string{}, d.Signals)
			if err != nil {
				hookCtx.WithError(err)
				hookCtx.Errorf("Hook exited with %d", code)
			} else {
				hookCtx.Infof("Hook exited with %d", code)
			}
		}
	}()

	return d.Queue.Receive(ch, d.ReceiveOpts)
}

func findHook(m Message, dir string) (string, error) {
	var hookBase string

	switch m.Transition {
	case instanceLaunchingEvent:
		hookBase = "launch"
	case instanceTerminatingEvent:
		hookBase = "terminate"
	default:
		return "", fmt.Errorf("Unexpected transition type %q", m.Transition)
	}

	path := filepath.Join(dir, hookBase)
	if _, err := os.Stat(path); err != nil {
		return "", err
	}

	return path, nil
}

func executeHook(command string, args []string, sigs chan os.Signal) (syscall.WaitStatus, error) {
	cmd := exec.Command(command, args...)
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
