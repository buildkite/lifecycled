package lifecycled

import "time"

type Message struct {
	Time        time.Time `json:"Time"`
	GroupName   string    `json:"AutoScalingGroupName"`
	InstanceID  string    `json:"EC2InstanceId"`
	ActionToken string    `json:"LifecycleActionToken"`
	Transition  string    `json:"LifecycleTransition"`
	HookName    string    `json:"LifecycleHookName"`
	Envelope    interface{}
}

type ReceiveOpts struct {
	VisibilityTimeout time.Duration
}

type Queue interface {
	Receive(ch chan Message, opts ReceiveOpts) error
	Delete(m Message) error
	Release(m Message) error
}

type simulatedQueue struct {
	InstanceID string
}

func NewSimulatedQueue(instanceID string) Queue {
	return &simulatedQueue{
		InstanceID: instanceID,
	}
}

func (sq *simulatedQueue) Receive(ch chan Message, opts ReceiveOpts) error {
	for t := range time.NewTicker(time.Millisecond * 500).C {
		ch <- Message{
			Time:        t,
			Transition:  instanceTerminatingEvent,
			InstanceID:  sq.InstanceID,
			GroupName:   "AFakeAutoscalingGroupName",
			ActionToken: "AFakeActionTokenxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
			HookName:    "AFakeHookToken",
		}
	}
	return nil
}

func (sq *simulatedQueue) Delete(m Message) error {
	return nil
}

func (sq *simulatedQueue) Release(m Message) error {
	return nil
}
