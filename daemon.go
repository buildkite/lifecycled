package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	log "github.com/sirupsen/logrus"
)

type Transition string

const (
	SpotTermination      Transition = "ec2:SPOT_INSTANCE_TERMINATION"
	LifecycleTermination Transition = "autoscaling:EC2_INSTANCE_TERMINATING"
	heartbeatFrequency              = time.Second * 10
)

type Notice struct {
	TargetInstanceID string
	Transition       Transition
	Message          *AutoscalingMessage
	Deadline         *time.Time
}

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
func (d *Daemon) Start(rootCtx context.Context) (completeFunc func() error, err error) {
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

	// Add a child context to cancel both polling go-routines when one
	// has returned and wait group to know when it's safe to close the channel
	ctx, stopPolling := context.WithCancel(rootCtx)
	defer stopPolling()
	var wg sync.WaitGroup

	// We are using a buffered channel to avoid deadlocking a goroutine when we stop listening
	notices := make(chan *Notice, 1)

	go d.pollTerminationNotice(ctx, &wg, notices)
	wg.Add(1)
	log.Info("Listening for lifecycle termination notices")

	go d.pollSpotTermination(ctx, &wg, notices)
	wg.Add(1)
	log.Info("Listening for spot termination notices")

	go func() {
		wg.Wait()
		close(notices)
	}()

	var notice *Notice
	for n := range notices {
		switch n.Transition {
		case LifecycleTermination:
			log.Info("Got a lifecycle hook termination notice")
			completeFunc = d.generateCompleteFunc(n)
		case SpotTermination:
			log.WithField("deadline", n.Deadline).Info("Got a spot instance termination notice")
		}
		notice = n
		break
	}
	stopPolling()

	// The handler should not be executed if the channels were closed because the context was cancelled.
	if rootCtx.Err() == context.Canceled {
		return nil, nil
	}

	log.Info("Executing handler")
	start := time.Now()
	err = d.executeHandler(ctx, []string{string(notice.Transition), notice.TargetInstanceID})
	logEntry := log.WithField("duration", time.Since(start))
	if err != nil {
		logEntry.WithError(err).Error("Handler script failed")
	} else {
		logEntry.Info("Handler finished successfully")
	}
	return completeFunc, nil
}

func (d *Daemon) pollTerminationNotice(ctx context.Context, wg *sync.WaitGroup, notices chan<- *Notice) {
	defer wg.Done()
	defer log.Info("Stopped listening for lifecycle termination notices")
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
					log.WithError(err).Warn("Failed to unmarshal envelope")
					continue
				}

				log.WithFields(log.Fields{
					"type":    env.Type,
					"subject": env.Subject,
				}).Debug("Received an SQS message")

				// unmarshal inner layer
				if err := json.Unmarshal([]byte(env.Message), &msg); err != nil {
					log.WithError(err).Warn("Failed to unmarshal autoscaling message")
					continue
				}

				if msg.InstanceID != d.InstanceID {
					log.WithField("target", msg.InstanceID).Debug("Skipping autoscaling event, doesn't match instance id")
					continue
				}

				if msg.Transition != string(LifecycleTermination) {
					log.WithField("transition", msg.Transition).Debug("Skipping autoscaling event, not a termination notice")
					continue
				}

				notices <- &Notice{
					TargetInstanceID: msg.InstanceID,
					Transition:       LifecycleTermination,
					Message:          &msg,
				}
				return
			}
		}
	}
}

func (d *Daemon) pollSpotTermination(ctx context.Context, wg *sync.WaitGroup, notices chan<- *Notice) {
	defer wg.Done()
	defer log.Info("Stopped listening for spot termination notices")
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
			notices <- &Notice{
				TargetInstanceID: d.InstanceID,
				Transition:       SpotTermination,
				Deadline:         t,
			}
			return
		}
	}
}

func (d *Daemon) generateCompleteFunc(n *Notice) func() error {
	ticker := time.NewTicker(heartbeatFrequency)
	go d.startHeartbeat(ticker, n)

	return func() error {
		defer ticker.Stop()

		_, err := d.AutoScaling.CompleteLifecycleAction(&autoscaling.CompleteLifecycleActionInput{
			AutoScalingGroupName:  aws.String(n.Message.GroupName),
			LifecycleHookName:     aws.String(n.Message.HookName),
			InstanceId:            aws.String(n.Message.InstanceID),
			LifecycleActionToken:  aws.String(n.Message.ActionToken),
			LifecycleActionResult: aws.String("CONTINUE"),
		})
		return err
	}
}

func (d *Daemon) startHeartbeat(ticker *time.Ticker, n *Notice) {
	for range ticker.C {
		log.Debug("Sending heartbeat")
		_, err := d.AutoScaling.RecordLifecycleActionHeartbeat(
			&autoscaling.RecordLifecycleActionHeartbeatInput{
				AutoScalingGroupName: aws.String(n.Message.GroupName),
				LifecycleHookName:    aws.String(n.Message.HookName),
				InstanceId:           aws.String(n.Message.InstanceID),
				LifecycleActionToken: aws.String(n.Message.ActionToken),
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
