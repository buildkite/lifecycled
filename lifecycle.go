package main

import (
	"context"
	"os"
	"os/exec"
	"sync"
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
	Handler     *os.File
}

func (l *LifecycleMonitor) Run(ctx context.Context) error {
	var cleanup sync.Once
	cleanupFunc := func() {
		if err := l.Queue.Delete(); err != nil {
			log.WithError(err).Error("Failed to delete queue")
		}

		if err := l.Queue.Unsubscribe(); err != nil {
			log.WithError(err).Error("Failed to unsubscribe from sns topic")
		}
	}

	// create an SQS queue for the upstream SNS topic
	if err := l.Queue.Create(); err != nil {
		return err
	}

	defer cleanup.Do(cleanupFunc)

	// connect the SQS queue to the SNS topic
	if err := l.Queue.Subscribe(); err != nil {
		return err
	}

	ch := make(chan *sqs.Message)

	go func() {
		log.Info("Listening for lifecycle notifications")
		if err := l.Queue.Receive(ctx, ch); err != nil {
			log.WithError(err).Error("Failed to receive from queue")
		}
	}()

	for m := range ch {
		var env envelope
		var msg autoscalingMessage

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

		if msg.InstanceID != l.InstanceID {
			log.WithFields(log.Fields{
				"was":    msg.InstanceID,
				"wanted": l.InstanceID,
			}).Debugf("Skipping autoscaling event, doesn't match instance id")
			continue
		}

		l.handleMessage(msg, func() {
			cleanup.Do(cleanupFunc)
		})
	}

	return nil
}

func (l *LifecycleMonitor) handleMessage(m autoscalingMessage, cleanup func()) {
	ctx := log.WithFields(log.Fields{
		"transition": m.Transition,
		"instanceid": m.InstanceID,
	})

	hbt := time.NewTicker(heartbeatFrequency)
	go func() {
		for range hbt.C {
			ctx.Debug("Sending heartbeat")
			if err := sendHeartbeat(l.AutoScaling, m); err != nil {
				ctx.WithError(err).Error("Heartbeat failed")
			}
		}
	}()

	handlerCtx := log.WithFields(log.Fields{
		"transition": m.Transition,
		"instanceid": m.InstanceID,
		"handler":    l.Handler.Name(),
	})

	handlerCtx.Info("Executing handler")
	timer := time.Now()

	cmd := exec.Command(l.Handler.Name(), m.Transition, m.InstanceID)
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	err := cmd.Run()

	executeCtx := handlerCtx.WithFields(log.Fields{
		"duration": time.Now().Sub(timer),
	})
	hbt.Stop()

	if err != nil {
		executeCtx.WithError(err).Error("Handler script failed")
		return
	}

	executeCtx.Info("Handler finished successfully")
	cleanup()

	if err = completeLifecycle(l.AutoScaling, m); err != nil {
		ctx.WithError(err).Error("Failed to complete lifecycle action")
		return
	}

	ctx.Info("Lifecycle action completed successfully")
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
