EC2 Lifecycle Listener
======================

A simple daemon that runs on an EC2 instance and subscribes to an [SQS][] queue and listens for [Auto Scaling Lifecycle events](https://docs.aws.amazon.com/AutoScaling/latest/DeveloperGuide/AutoScalingGroupLifecycle.html). The events are dispatched to shell scripts in a hooks directory. The lifecycle event will be delayed until the shell scripts finish executing, at which point the lifecycle event will be completed.

## Developing

```
go run ./cli/lifecycled/*.go --queue simulate --hooks ./hooks/ --instanceid llamas
```
