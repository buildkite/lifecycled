package lifecycled

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/sirupsen/logrus"
)

const (
	// sqsErrorBackoff paces the polling loop when GetMessages keeps failing, so a
	// persistent error (throttling, permissions) doesn't spin the loop.
	sqsErrorBackoff = 5 * time.Second

	// cleanupTimeout bounds the queue and subscription teardown that runs on a
	// fresh context during shutdown. It is short and shared across both calls so
	// cleanup can't delay the termination handler or outlast the supervisor's
	// stop timeout when an endpoint is slow or unreachable.
	cleanupTimeout = 5 * time.Second

	// awsActionTimeout bounds CompleteLifecycleAction, which runs on a fresh
	// context after the handler returns, so an unreachable endpoint can't wedge
	// the process while leaving room for the SDK's default retries to land.
	awsActionTimeout = 30 * time.Second
)

// AutoscalingClient is the subset of the EC2 Auto Scaling API used by the daemon.
//
//go:generate go tool mockgen -destination=mocks/mock_autoscaling_client.go -package=mocks github.com/buildkite/lifecycled AutoscalingClient
type AutoscalingClient interface {
	CompleteLifecycleAction(context.Context, *autoscaling.CompleteLifecycleActionInput, ...func(*autoscaling.Options)) (*autoscaling.CompleteLifecycleActionOutput, error)
	RecordLifecycleActionHeartbeat(context.Context, *autoscaling.RecordLifecycleActionHeartbeatInput, ...func(*autoscaling.Options)) (*autoscaling.RecordLifecycleActionHeartbeatOutput, error)
}

// Envelope ...
type Envelope struct {
	Type    string    `json:"Type"`
	Subject string    `json:"Subject"`
	Time    time.Time `json:"Time"`
	Message string    `json:"Message"`
}

// Message ...
type Message struct {
	Time        time.Time `json:"Time"`
	GroupName   string    `json:"AutoScalingGroupName"`
	InstanceID  string    `json:"EC2InstanceId"`
	ActionToken string    `json:"LifecycleActionToken"`
	Transition  string    `json:"LifecycleTransition"`
	HookName    string    `json:"LifecycleHookName"`
}

// NewAutoscalingListener ...
func NewAutoscalingListener(instanceID string, queue *Queue, autoscaling AutoscalingClient, heartbeatInterval time.Duration) *AutoscalingListener {
	return &AutoscalingListener{
		listenerType:      "autoscaling",
		instanceID:        instanceID,
		queue:             queue,
		autoscaling:       autoscaling,
		heartbeatInterval: heartbeatInterval,
	}
}

// AutoscalingListener ...
type AutoscalingListener struct {
	listenerType      string
	instanceID        string
	queue             *Queue
	autoscaling       AutoscalingClient
	heartbeatInterval time.Duration
}

// Type returns a string describing the listener type.
func (l *AutoscalingListener) Type() string {
	return l.listenerType
}

// Start the autoscaling lifecycle hook listener.
func (l *AutoscalingListener) Start(ctx context.Context, notices chan<- TerminationNotice, log *logrus.Entry) error {
	log.WithField("queue", l.queue.name).Debug("Creating sqs queue")
	if err := l.queue.Create(ctx); err != nil {
		return err
	}
	subscribed := false
	// Tear down the subscription and queue on a fresh, bounded context so cleanup
	// still runs after ctx is cancelled during shutdown, sharing one short deadline
	// so a slow endpoint can't delay the handler or outlast the supervisor's stop
	// timeout.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if subscribed {
			log.WithField("arn", l.queue.subscriptionArn).Debug("Deleting sns subscription")
			if err := l.queue.Unsubscribe(cleanupCtx); err != nil {
				log.WithError(err).Error("Failed to unsubscribe from sns topic")
			}
		}
		log.WithField("queue", l.queue.name).Debug("Deleting sqs queue")
		if err := l.queue.Delete(cleanupCtx); err != nil {
			log.WithError(err).Error("Failed to delete queue")
		}
	}()

	log.WithField("topic", l.queue.topicArn).Debug("Subscribing queue to sns topic")
	if err := l.queue.Subscribe(ctx); err != nil {
		return err
	}
	subscribed = true

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			log.WithField("queueURL", l.queue.url).Debug("Polling sqs for messages")
			messages, err := l.queue.GetMessages(ctx)
			if err != nil {
				log.WithError(err).Warn("Failed to get messages from SQS")
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(sqsErrorBackoff):
				}
				continue
			}
			for _, m := range messages {
				var env Envelope
				var msg Message

				if err := l.queue.DeleteMessage(ctx, aws.ToString(m.ReceiptHandle)); err != nil {
					log.WithError(err).Warn("Failed to delete message")
				}

				// unmarshal outer layer
				if err := json.Unmarshal([]byte(aws.ToString(m.Body)), &env); err != nil {
					log.WithError(err).Error("Failed to unmarshal envelope")
					continue
				}

				log.WithFields(logrus.Fields{
					"type":    env.Type,
					"subject": env.Subject,
				}).Debug("Received an SQS message")

				// unmarshal inner layer
				if err := json.Unmarshal([]byte(env.Message), &msg); err != nil {
					log.WithError(err).Error("Failed to unmarshal autoscaling message")
					continue
				}

				if msg.InstanceID != l.instanceID {
					log.WithField("target", msg.InstanceID).Debug("Skipping autoscaling event, doesn't match instance id")
					continue
				}

				if msg.Transition != "autoscaling:EC2_INSTANCE_TERMINATING" {
					log.WithField("transition", msg.Transition).Debug("Skipping autoscaling event, not a termination notice")
					continue
				}

				notices <- &autoscalingTerminationNotice{
					noticeType:        l.Type(),
					message:           &msg,
					autoscaling:       l.autoscaling,
					heartbeatInterval: l.heartbeatInterval,
				}
				return nil
			}
		}
	}
}

type autoscalingTerminationNotice struct {
	noticeType        string
	message           *Message
	autoscaling       AutoscalingClient
	heartbeatInterval time.Duration
}

func (n *autoscalingTerminationNotice) Type() string {
	return n.noticeType
}

func (n *autoscalingTerminationNotice) Handle(ctx context.Context, handler Handler, log *logrus.Entry) error {
	defer func() {
		// Fresh, bounded context so completion runs even if ctx was cancelled mid-shutdown.
		completeCtx, cancel := context.WithTimeout(context.Background(), awsActionTimeout)
		defer cancel()
		_, err := n.autoscaling.CompleteLifecycleAction(completeCtx, &autoscaling.CompleteLifecycleActionInput{
			AutoScalingGroupName:  aws.String(n.message.GroupName),
			LifecycleHookName:     aws.String(n.message.HookName),
			InstanceId:            aws.String(n.message.InstanceID),
			LifecycleActionToken:  aws.String(n.message.ActionToken),
			LifecycleActionResult: aws.String("CONTINUE"),
		})
		if err != nil {
			log.WithError(err).Error("Failed to complete lifecycle action")
		} else {
			log.Info("Lifecycle action completed successfully")
		}
	}()

	ticker := time.NewTicker(n.heartbeatInterval)
	defer ticker.Stop()

	// Stop the heartbeat goroutine when Handle returns; ticker.Stop alone doesn't
	// close the channel, so a bare "for range ticker.C" would park forever.
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()

	go func() {
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				log.Debug("Sending heartbeat")
				_, err := n.autoscaling.RecordLifecycleActionHeartbeat(
					heartbeatCtx,
					&autoscaling.RecordLifecycleActionHeartbeatInput{
						AutoScalingGroupName: aws.String(n.message.GroupName),
						LifecycleHookName:    aws.String(n.message.HookName),
						InstanceId:           aws.String(n.message.InstanceID),
						LifecycleActionToken: aws.String(n.message.ActionToken),
					},
				)
				// A heartbeat cancelled because Handle returned is a clean stop, not
				// a failure worth logging.
				if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					log.WithError(err).Warn("Failed to send heartbeat")
				}
			}
		}
	}()

	return handler.Execute(ctx, n.message.Transition, n.message.InstanceID)
}
