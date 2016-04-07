package lifecycled

import (
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/autoscaling"
)

const (
	instanceLaunchingEvent   = "autoscaling:EC2_INSTANCE_LAUNCHING"
	instanceTerminatingEvent = "autoscaling:EC2_INSTANCE_TERMINATING"
)

func sendHeartbeat(svc *autoscaling.AutoScaling, m Message) error {
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

func completeLifecycle(svc *autoscaling.AutoScaling, m Message) error {
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
