# Lifecycled [![Build status](https://badge.buildkite.com/59427d66eb5576325ded875cae10b6cfcc0a63c6dd49ec4ec8.svg?branch=master)](https://buildkite.com/buildkite/lifecycled)

Gracefully handles EC2 scaling events. Lifecycled is designed to run on an AWS EC2 instance and listen for various state change mechanisms:

 * [AWS AutoScaling](https://docs.aws.amazon.com/autoscaling/ec2/userguide/lifecycle-hooks.html)
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
AWS_REGION=us-east-1
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

### Handler Arguments
The handler script is passed the event that was received and the instance id, e.g `autoscaling:EC2_INSTANCE_TERMINATING i-001405f0fc67e3b12` for lifecycle events, or `ec2:SPOT_INSTANCE_TERMINATION i-001405f0fc67e3b12 2015-01-05T18:02:00Z` in the case of a spot termination.

Additional arguments can be provided to lifecycled using the environment variable `LIFECYCLED_HANDLER_ARGS` or the flag `--handler-args` flag one or more times. When using additional arguments, the default arguments will be appended to the end of the argument list.

E.g. The following instance of lifecycled will pass the handler script arguments `argument1 argument2 autoscaling:EC2_INSTANCE_TERMINATING i-001405f0fc67e3b12`

```bash
lifecycled --handler-args "argument1" --handler-args "argument2"
```

### Handlers on Windows
Due to the nature of executables in Windows, a Powershell script can’t be passed as a handler directly without additional configuration to your Windows registry. Instead you can use Powershell as the executable handler and a separate Powershell script as an additional argument which will be executed. The Powershell script executed will receive any additional arguments, such as the event and instance id.

```powershell
$env:LIFECYCLED_HANDLER="$env:SYSTEMROOT\System32\WindowsPowerShell\v1.0\powershell.exe"
$env:LIFECYCLED_HANDLER_ARGS="$env:ProgramData\lifecycled\handler.ps1"
$env:LIFECYCLED_SNS_TOPIC="arn:aws:sns:us-east-1:11111111:my-lifecycle-topic"
$env:AWS_REGION="us-east-1"


.\lifecycled-windows-amd64.exe
```

## Licence

See [Licence](LICENSE) (MIT)
