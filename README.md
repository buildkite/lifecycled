# Lifecycled - Gracefully handle EC2 scaling events

Lifecycled is designed to run on an AWS EC2 instance and listen for various state change mechanisms:

 * [AWS AutoScaling](https://docs.aws.amazon.com/AutoScaling/latest/DeveloperGuide/lifecycle-hooks.html)
 * [Spot Instance Termination Notices](http://docs.aws.amazon.com/AWSEC2/latest/UserGuide/spot-interruptions.html)

When a termination notice is received, lifecycled runs a user-provided script (called a handler) and then proceeds with the shutdown. This script can be used to gracefully terminate any daemons you have running.

## Installing with Systemd

Either install with `go get -u github.com/buildkite/lifecycled` or download a [binary release for Linux or Windows](https://github.com/buildkite/lifecycled/releases). Install into `/usr/bin/lifecycled`.

```bash
# Install the binary
curl -Lf -o /usr/bin/lifecycled \
	https://github.com/buildkite/lifecycled/releases/download/${VERSION}/lifecycled-linux-amd64
chmod +x /usr/bin/lifecycled

# Install the systemd service
touch /etc/lifecycled
curl -Lf -o /etc/systemd/system/lifecycled.service \
	https://raw.githubusercontent.com/buildkite/lifecycled/${VERSION}/init/systemd/lifecycled.unit
```

Assuming your custom handler script is in `/usr/local/bin/my_graceful_shutdown.sh` and you've got an SNS topic for your EC2 Lifecycle Hooks, you would configure `/etc/lifecycled` with:

```bash
LIFECYCLED_HANDLER=/usr/local/bin/my_graceful_shutdown.sh
LIFECYCLED_SNS_TOPIC=arn:aws:sns:us-east-1:11111111:my-lifecycle-topic
```

Then start the daemon with:

```bash
systemctl daemon-reload
systemctl enable lifecycled
systemctl start lifecycled
systemctl status lifecycled
```

## Handler script

Handler scripts are used for things like shutting down services that need some time to shutdown. Any example script that shuts down a service and waits for it to shutdown might look like:

```bash
#!/bin/bash
set -euo pipefail
function await_shutdown() {
  echo -n "Waiting for $1..."
  while systemctl is-active $1 > /dev/null; do
    sleep 1
  done
  echo "Done!"
}
systemctl stop myservice.service
await_shutdown myservice.service
```

The handler script is passed the event that was received and the instance id, e.g `autoscaling:EC2_INSTANCE_TERMINATING i-001405f0fc67e3b12` for lifecycle events, or `ec2:SPOT_INSTANCE_TERMINATION i-001405f0fc67e3b12 2015-01-05T18:02:00Z` in the case of a spot termination.

## Cleaning up Leftover SQS Queues

The `lifecycled` daemon should clean up the per-instance SQS queues that are created when it shuts down, but there has been a bug where this does not happen (See https://github.com/buildkite/lifecycled/issues/12). To mitigate this, you can run a cleanup tool:

```
go get -u github.com/buildkite/lifecycled/tools/lifecycled-queue-cleaner
lifecycled-queue-cleaner
```

## FAQ

Q: How can i start up lifecycled?\
A: You can config enviroment variables or set command line flags, eg:
```bash
./lifecycled-linux-amd64 --handler=HANDLER --sns-topic=SNS-TOPIC
```


Q: What is the environment variables?\
A:

|Variable             |Value|
|---------------------|-----|
|LIFECYCLED_HANDLER   | The script to invoke to handle events, eg: _/home/ubuntu/autoscalling_terminate.sh_ |
|LIFECYCLED_SNS_TOPIC | The SNS topic that receives events, eg: _arn:aws:sns:*_ |
|AWS_REGION           | You AWS Region, eg: _us-east-1_ |
|AWS_ACCESS_KEY_ID    | Your AWS Access Key ID |
|AWS_SECRET_ACCESS_KEY| Your AWS Secret Access Key |

Q: How to run on Windows?\
A: Set up environment variables or pass command line flags.

## Licence

See [Licence.md](Licence.md) (MIT)
