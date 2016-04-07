AWS AutoScaling Lifecycle Hook Daemon
=====================================

[AWS AutoScaling](https://docs.aws.amazon.com/AutoScaling/latest/DeveloperGuide/lifecycle-hooks.html) provides a mechanism for performing custom actions when Auto Scaling launches or terminates an instance in your AutoScaling group. `lifecycled` provides a way to consume these events and respond with simple shell scripts.

Lifecycle events are consumed from an SQS queue and a corresponding hook is executed. Whilst the hook is executing `lifecycled` sends heartbeats to the Autoscaling group to stall further action. When the hook completes executing, if it completes successfully the lifecycle action is completed successfully, if the hook returns a non-zero exit code then the lifecycle action is abandoned.

## Developing

```
go run ./cli/lifecycled/*.go --queue simulate --hooks ./hooks/ --instanceid llamas
```
