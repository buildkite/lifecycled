Lifecycled - Gracefully handle EC2 scaling events
=======================================================

Lifecycled is designed to run on an AWS EC2 instance and listen for various state change mechanisms:

 * [AWS AutoScaling](https://docs.aws.amazon.com/AutoScaling/latest/DeveloperGuide/lifecycle-hooks.html)
 * [Spot Instance Termination Notices](http://docs.aws.amazon.com/AWSEC2/latest/UserGuide/spot-interruptions.html)

A handler script is provided to gracefully handle these actions before shutdown.

Installing
----------

Either install with `go get -u github.com/buildkite/lifecycled` or download a [binary release for Linux or Windows](https://github.com/buildkite/lifecycled/releases).

Running
-------

Check out the [Cloudformation Template](cloudformation/template.yml) for an example of how to setup an autoscaling group with the right permissions and an SNS topic. Once you have the SNS topic, run lifecycled on your aws instance:

```
lifecycled --sns-topic arn:aws:sns:us-east-1:11111111:lifecycled-test-1501806648-LifecycleTopic-UTAZ7PQOA32Q
```

Autoscaling Hooks
-----------------

Lifecycle events are consumed from an SQS queue and a corresponding hook is executed. Whilst the hook is executing `lifecycled` sends heartbeats to the Autoscaling group to stall further action. When the hook completes executing, if it completes successfully the lifecycle action is completed successfully, if the hook returns a non-zero exit code then the lifecycle action is abandoned.

The handler script gets passed the event and the instance id, e.g: `autoscaling:EC2_INSTANCE_TERMINATING i-001405f0fc67e3b12`

Spot Termination
----------------

These notices are consumed by polling the local metadata url every 5 seconds. They are passed to the handler script as a custom event, the instance id and the timestamp, e.g `ec2:SPOT_INSTANCE_TERMINATION i-001405f0fc67e3b12 2015-01-05T18:02:00Z`

Cleaning up Leftover SQS Queues
-------------------------------

The `lifecycled` daemon should clean up the per-instance SQS queues that are created when it shuts down, but there has been a bug where this does not happen (See https://github.com/buildkite/lifecycled/issues/12). To mitigate this, you can run a cleanup tool:

```
go get -u github.com/buildkite/lifecycled/tools/lifecycled-queue-cleaner
lifecycled-queue-cleaner
```
