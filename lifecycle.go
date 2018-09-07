package main

import (
	"context"
	"time"

	"encoding/json"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/sqs"

	log "github.com/sirupsen/logrus"
)

const (
	heartbeatFrequency = time.Second * 10
)

type envelope struct {
	Type    string    `json:"Type"`
	Subject string    `json:"Subject"`
	Time    time.Time `json:"Time"`
	Message string    `json:"Message"`
}

type autoscalingMessage struct {
	Time        time.Time `json:"Time"`
	GroupName   string    `json:"AutoScalingGroupName"`
	InstanceID  string    `json:"EC2InstanceId"`
	ActionToken string    `json:"LifecycleActionToken"`
	Transition  string    `json:"LifecycleTransition"`
	HookName    string    `json:"LifecycleHookName"`
}

type LifecycleMonitor struct {
	InstanceID  string
	Queue       *Queue
	AutoScaling *autoscaling.AutoScaling
}

func (l *LifecycleMonitor) create() error {
	log.Debug("Creating lifecycle queue")

	if err := l.Queue.Create(); err != nil {
		return err
	}

	if err := l.Queue.Subscribe(); err != nil {
		return err
	}

	return nil
}

func (l *LifecycleMonitor) destroy() error {
	log.Debug("Cleaning up lifecycle queue")

	if err := l.Queue.Unsubscribe(); err != nil {
		return err
	}

	if err := l.Queue.Delete(); err != nil {
		return err
	}

	return nil
}

func (l *LifecycleMonitor) Run(ctx context.Context, termCh chan TerminationNotice) error {
	if err := l.create(); err != nil {
		return err
	}

	ch := make(chan *sqs.Message)

	go func() {
		log.Info("Listening for lifecycle notifications")
		if err := l.Queue.Receive(ctx, ch); err != nil {
			log.WithError(err).Error("Failed to receive from queue")
		}
	}()

	for sqsMessage := range ch {
		msg, err := l.parseQueueMessage(sqsMessage)
		if err != nil {
			log.WithError(err).Error("Failed to parse SQS message")
		}

		if msg.InstanceID != l.InstanceID {
			log.WithFields(log.Fields{
				"was":    msg.InstanceID,
				"wanted": l.InstanceID,
			}).Debugf("Skipping autoscaling event, doesn't match instance id")
			continue
		}

		messageLogCtx := log.WithFields(log.Fields{
			"transition": msg.Transition,
			"instanceid": msg.InstanceID,
		})

		hbt := time.NewTicker(heartbeatFrequency)
		go func() {
			for range hbt.C {
				messageLogCtx.Debug("Sending heartbeat")
				if err := sendHeartbeat(l.AutoScaling, msg); err != nil {
					messageLogCtx.WithError(err).Error("Heartbeat failed")
				}
			}
		}()

		doneCh := make(chan struct{})
		errCh := make(chan error)

		termCh <- TerminationNotice{
			Done:  doneCh,
			Error: errCh,
			Args:  []string{msg.Transition, msg.InstanceID},
		}

		select {
		case <-doneCh:
			hbt.Stop()

			// try and destroy the queue as soon as we can
			if err := l.destroy(); err != nil {
				log.WithError(err).Error("Failed to destroy lifecycle monitor")
			}

			if err = completeLifecycle(l.AutoScaling, msg); err != nil {
				messageLogCtx.WithError(err).Error("Failed to complete lifecycle action")
				return err
			}

			return nil

		case <-errCh:
			hbt.Stop()
		}
	}

	return l.destroy()
}

func (l *LifecycleMonitor) parseQueueMessage(m *sqs.Message) (autoscalingMessage, error) {
	var env envelope
	var msg autoscalingMessage

	// unmarshal outer layer
	if err := json.Unmarshal([]byte(*m.Body), &env); err != nil {
		return msg, err
	}

	// unmarshal inner layer
	if err := json.Unmarshal([]byte(env.Message), &msg); err != nil {
		return msg, err
	}

	return msg, nil
}

func sendHeartbeat(svc *autoscaling.AutoScaling, m autoscalingMessage) error {
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

func completeLifecycle(svc *autoscaling.AutoScaling, m autoscalingMessage) error {
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
