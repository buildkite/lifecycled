package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	log "github.com/sirupsen/logrus"
)

const (
	heartbeatFrequency = time.Second * 10
)

type Envelope struct {
	Type    string    `json:"Type"`
	Subject string    `json:"Subject"`
	Time    time.Time `json:"Time"`
	Message string    `json:"Message"`
}

type AutoscalingMessage struct {
	Time        time.Time `json:"Time"`
	GroupName   string    `json:"AutoScalingGroupName"`
	InstanceID  string    `json:"EC2InstanceId"`
	ActionToken string    `json:"LifecycleActionToken"`
	Transition  string    `json:"LifecycleTransition"`
	HookName    string    `json:"LifecycleHookName"`
}

type Daemon struct {
	InstanceID  string
	Queue       *Queue
	AutoScaling *autoscaling.AutoScaling
	Handler     *os.File
	Signals     chan os.Signal
}

// Start the daemon.
func (d *Daemon) Start(ctx context.Context) error {
	if err := d.Queue.Create(); err != nil {
		return err
	}
	defer func() {
		if err := d.Queue.Delete(); err != nil {
			log.WithError(err).Error("Failed to delete queue")
		}
	}()

	if err := d.Queue.Subscribe(); err != nil {
		return err
	}
	defer func() {
		if err := d.Queue.Unsubscribe(); err != nil {
			log.WithError(err).Error("Failed to unsubscribe from sns topic")
		}
	}()

	lifecycleTermination := make(chan AutoscalingMessage)
	go d.pollTerminationNotice(ctx, lifecycleTermination)

	spotTermination := make(chan *time.Time)
	go d.pollSpotTermination(ctx, spotTermination)

	log.Info("Listening for lifecycle notifications")
	var (
		targetInstanceID    string
		lifecycleTransition string
	)

Listener:
	for {
		select {
		case <-ctx.Done():
			return nil
		case notice := <-lifecycleTermination:
			log.Infof("Got a lifecycle hook termination notice")
			targetInstanceID, lifecycleTransition = notice.InstanceID, notice.Transition
			break Listener
		case notice := <-spotTermination:
			log.Infof("Got a spot instance termination notice: %v", notice)
			targetInstanceID, lifecycleTransition = d.InstanceID, terminationTransition
			break Listener
		}
	}

	log.Info("Executing handler")
	start := time.Now()
	err := executeHandler(d.Handler, []string{lifecycleTransition, targetInstanceID}, d.Signals)
	logEntry := log.WithField("duration", time.Now().Sub(start))
	if err != nil {
		logEntry.WithError(err).Error("Handler script failed")
	} else {
		logEntry.Info("Handler finished successfully")
	}
	return nil
}

func (d *Daemon) pollTerminationNotice(ctx context.Context, notices chan<- AutoscalingMessage) {
	log.Debugf("Polling lifecycle hook for termination notices")
	defer close(notices)
	for {
		select {
		case <-ctx.Done():
			return
		default:
			messages, err := d.Queue.GetMessages(ctx)
			if err != nil {
				log.WithError(err).Warn("Failed to get messages from SQS")
			}
			for _, m := range messages {
				var env Envelope
				var msg AutoscalingMessage

				if err := d.Queue.DeleteMessage(ctx, aws.StringValue(m.ReceiptHandle)); err != nil {
					log.WithError(err).Warn("Failed to delete message")
				}

				// unmarshal outer layer
				if err := json.Unmarshal([]byte(*m.Body), &env); err != nil {
					log.WithError(err).Info("Failed to unmarshal envelope")
					continue
				}

				log.WithFields(log.Fields{
					"type":    env.Type,
					"subject": env.Subject,
				}).Debugf("Received an SQS message")

				// unmarshal inner layer
				if err := json.Unmarshal([]byte(env.Message), &msg); err != nil {
					log.WithError(err).Info("Failed to unmarshal autoscaling message")
					continue
				}

				if msg.InstanceID != d.InstanceID {
					log.WithFields(log.Fields{
						"was":    msg.InstanceID,
						"wanted": d.InstanceID,
					}).Debugf("Skipping autoscaling event, doesn't match instance id")
					continue
				}
				notices <- msg
				return
			}
		}
	}
}

func (d *Daemon) pollSpotTermination(ctx context.Context, notices chan<- *time.Time) {
	log.Debugf("Polling metadata for spot termination notices")
	defer close(notices)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.NewTicker(time.Second * 5).C:
			t, err := getSpotTermination(ctx)
			if err != nil {
				log.WithError(err).Error("Failed to get spot termination")
			}
			if t == nil {
				continue
			}
			notices <- t
			return
		}
	}
}

func (d *Daemon) handleMessage(m AutoscalingMessage) {
	ctx := log.WithFields(log.Fields{
		"transition": m.Transition,
		"instanceid": m.InstanceID,
	})

	hbt := time.NewTicker(heartbeatFrequency)
	go func() {
		for range hbt.C {
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

	err := executeHandler(d.Handler, []string{m.Transition, m.InstanceID}, d.Signals)
	executeCtx := handlerCtx.WithFields(log.Fields{
		"duration": time.Now().Sub(timer),
	})
	hbt.Stop()

	if err != nil {
		executeCtx.WithError(err).Error("Handler script failed")
		return
	}

	executeCtx.Info("Handler finished successfully")

	if err = completeLifecycle(d.AutoScaling, m); err != nil {
		ctx.WithError(err).Error("Failed to complete lifecycle action")
		return
	}

	ctx.Info("Lifecycle action completed successfully")
}

func executeHandler(command *os.File, args []string, sigs chan os.Signal) error {
	cmd := exec.Command(command.Name(), args...)
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	go func() {
		sig := <-sigs
		if cmd.Process != nil {
			cmd.Process.Signal(sig)
		}
	}()

	return cmd.Run()
}

func sendHeartbeat(svc *autoscaling.AutoScaling, m AutoscalingMessage) error {
	_, err := svc.RecordLifecycleActionHeartbeat(&autoscaling.RecordLifecycleActionHeartbeatInput{
		AutoScalingGroupName: aws.String(m.GroupName),
		LifecycleHookName:    aws.String(m.HookName),
		InstanceId:           aws.String(m.InstanceID),
		LifecycleActionToken: aws.String(m.ActionToken),
	})
	if err != nil {
		return err
	}
	return nil
}

func completeLifecycle(svc *autoscaling.AutoScaling, m AutoscalingMessage) error {
	_, err := svc.CompleteLifecycleAction(&autoscaling.CompleteLifecycleActionInput{
		AutoScalingGroupName:  aws.String(m.GroupName),
		LifecycleHookName:     aws.String(m.HookName),
		InstanceId:            aws.String(m.InstanceID),
		LifecycleActionToken:  aws.String(m.ActionToken),
		LifecycleActionResult: aws.String("CONTINUE"),
	})
	if err != nil {
		return err
	}
	return nil
}
