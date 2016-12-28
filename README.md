AWS AutoScaling Lifecycle Hook Daemon [![wercker status](https://app.wercker.com/status/53e4d9070a3b038c7b6aa98b3a2294f1/s/master "wercker status")](https://app.wercker.com/project/byKey/53e4d9070a3b038c7b6aa98b3a2294f1)
=====================================

[AWS AutoScaling](https://docs.aws.amazon.com/AutoScaling/latest/DeveloperGuide/lifecycle-hooks.html) provides a mechanism for performing custom actions when Auto Scaling launches or terminates an instance in your AutoScaling group. `lifecycled` provides a way to consume these events and respond with simple shell scripts.

Lifecycle events are consumed from an SQS queue and a corresponding hook is executed. Whilst the hook is executing `lifecycled` sends heartbeats to the Autoscaling group to stall further action. When the hook completes executing, if it completes successfully the lifecycle action is completed successfully, if the hook returns a non-zero exit code then the lifecycle action is abandoned.

## Developing

```bash
go run ./cli/lifecycled/*.go --queue simulate --handler ./handler.sh --instanceid llamas
```

## Releasing
### Building binary
```bash
docker build --tag lifecycled-builder release/
docker run --rm -v "$PWD":/go/src/github.com/lox/lifecycled lifecycled-builder build.sh
ls -al builds/
```
### Building package

```bash
docker build --tag lifecycled-builder release/
docker run -v "$PWD":/go/src/github.com/lox/lifecycled -v "$PWD/output":/go/src/output -e LIFECYCLE_QUEUE=yourqueue -e AWS_REGION=yourregion lifecycled-builder pkg-builder.sh $VERSION
ls -al output/
```
