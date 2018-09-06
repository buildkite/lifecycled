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
}

// Start the daemon.
func (d *Daemon) Start(ctx context.Context) (completeFunc func() error, err error) {
	if err := d.Queue.Create(); err != nil {
		return nil, err
	}
	defer func() {
		if err := d.Queue.Delete(); err != nil {
			log.WithError(err).Error("Failed to delete queue")
		}
	}()

	if err := d.Queue.Subscribe(); err != nil {
		return nil, err
	}
	defer func() {
		if err := d.Queue.Unsubscribe(); err != nil {
			log.WithError(err).Error("Failed to unsubscribe from sns topic")
		}
	}()

	// Add a child context to cancel both polling go-routines when one has returned
	pollCtx, stopPolling := context.WithCancel(ctx)
	defer stopPolling()

	lifecycleTermination := make(chan *AutoscalingMessage, 1)
	go d.pollTerminationNotice(pollCtx, lifecycleTermination)
	log.Info("Listening for lifecycle termination notices")

	spotTermination := make(chan *time.Time, 1)
	go d.pollSpotTermination(pollCtx, spotTermination)
	log.Info("Listening for spot termination notices")

	var (
		targetInstanceID    string
		lifecycleTransition string
	)

Listener:
	for {
		select {
		case notice, open := <-lifecycleTermination:
			if !open {
				lifecycleTermination = nil
				break
			}
			log.Infof("Got a lifecycle hook termination notice")
			targetInstanceID, lifecycleTransition = notice.InstanceID, notice.Transition

			// Start the heartbeat
			hbt := time.NewTicker(heartbeatFrequency)
			go d.startHeartbeat(hbt, notice)

			// Generate the completeFunc
			completeFunc = d.newCompleteFunc(hbt, notice)
		case notice, open := <-spotTermination:
			if !open {
				spotTermination = nil
				break
			}
			log.Infof("Got a spot instance termination notice: %v", notice)
			targetInstanceID, lifecycleTransition = d.InstanceID, terminationTransition
		}

		// Source: https://stackoverflow.com/a/13666733
		if lifecycleTermination == nil || spotTermination == nil {
			stopPolling()
			break Listener
		}
	}

	// The handler should not be executed if the channels were closed because the context was cancelled.
	if ctx.Err() == context.Canceled {
		return nil, nil
	}

	// Execute the handler
	log.Info("Executing handler")
	start := time.Now()
	err = d.executeHandler(ctx, []string{lifecycleTransition, targetInstanceID})
	logEntry := log.WithField("duration", time.Since(start))
	if err != nil {
		logEntry.WithError(err).Error("Handler script failed")
	} else {
		logEntry.Info("Handler finished successfully")
	}
	return completeFunc, nil
}

func (d *Daemon) pollTerminationNotice(ctx context.Context, notices chan<- *AutoscalingMessage) {
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
				notices <- &msg
				return
			}
		}
	}
}

func (d *Daemon) pollSpotTermination(ctx context.Context, notices chan<- *time.Time) {
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

func (d *Daemon) startHeartbeat(ticker *time.Ticker, m *AutoscalingMessage) {
	for range ticker.C {
		log.Debug("Sending heartbeat")
		_, err := d.AutoScaling.RecordLifecycleActionHeartbeat(
			&autoscaling.RecordLifecycleActionHeartbeatInput{
				AutoScalingGroupName: aws.String(m.GroupName),
				LifecycleHookName:    aws.String(m.HookName),
				InstanceId:           aws.String(m.InstanceID),
				LifecycleActionToken: aws.String(m.ActionToken),
			},
		)
		if err != nil {
			log.WithError(err).Error("Heartbeat failed")
		}
	}
}

func (d *Daemon) executeHandler(ctx context.Context, args []string) error {
	cmd := exec.Command(d.Handler.Name(), args...)
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	go func() {
		<-ctx.Done()
		if cmd.Process != nil {
			cmd.Process.Signal(os.Interrupt)
		}
	}()

	return cmd.Run()
}

func (d *Daemon) newCompleteFunc(ticker *time.Ticker, m *AutoscalingMessage) func() error {
	return func() error {
		defer ticker.Stop()

		_, err := d.AutoScaling.CompleteLifecycleAction(&autoscaling.CompleteLifecycleActionInput{
			AutoScalingGroupName:  aws.String(m.GroupName),
			LifecycleHookName:     aws.String(m.HookName),
			InstanceId:            aws.String(m.InstanceID),
			LifecycleActionToken:  aws.String(m.ActionToken),
			LifecycleActionResult: aws.String("CONTINUE"),
		})
		return err
	}
}
