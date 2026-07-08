# Lifecycled

[![Build status](https://badge.buildkite.com/59427d66eb5576325ded875cae10b6cfcc0a63c6dd49ec4ec8.svg?branch=master)](https://buildkite.com/buildkite/lifecycled)

Lifecycled is a daemon that gracefully handles AWS EC2 instance lifecycle events. It runs on EC2 instances and listens for termination events, giving your applications time to shut down cleanly before the instance is terminated.

## Features

- **AWS AutoScaling Lifecycle Hooks**: Intercepts and handles [AutoScaling termination lifecycle hooks](https://docs.aws.amazon.com/autoscaling/ec2/userguide/lifecycle-hooks.html)
- **Spot Instance Termination**: Monitors and responds to [EC2 Spot Instance termination notices](http://docs.aws.amazon.com/AWSEC2/latest/UserGuide/spot-interruptions.html)
- **Custom Handler Scripts**: Execute custom scripts to gracefully shutdown services
- **CloudWatch Logging**: Optional integration with CloudWatch Logs
- **SQS Queue Tagging**: Support for tagging SQS queues for cost allocation and organization
- **Automatic Instance Discovery**: Auto-detects instance ID and region from EC2 metadata
- **Configurable Intervals**: Customize polling and heartbeat intervals

## How It Works

Lifecycled runs as a daemon on your EC2 instances and:

1. **For AutoScaling Events**: Creates an SQS queue and subscribes it to your SNS topic that receives lifecycle events. When a termination event is received, it:
   - Executes your handler script
   - Sends periodic heartbeats to AWS to extend the timeout
   - Completes the lifecycle action when the handler finishes

   AutoScaling notices are handled at most once: the SQS message is deleted on receipt, before the handler runs. If lifecycled is killed mid-drain it does not resume on restart; the instance waits out the hook's `HeartbeatTimeout`, then the ASG applies its default result, so size that timeout accordingly. (Spot notices differ: they are re-read from instance metadata each poll, so a restarted daemon re-runs the handler.)

2. **For Spot Terminations**: Polls the EC2 instance metadata service for spot termination notices. When detected:
   - Executes your handler script
   - Allows graceful shutdown before AWS terminates the instance

## Installation

### Download Pre-built Binary

Download the latest release from the [GitHub releases page](https://github.com/buildkite/lifecycled/releases):

```bash
# Replace ${VERSION} with the desired version (e.g., v3.3.0)
curl -Lf -o /usr/bin/lifecycled \
  https://github.com/buildkite/lifecycled/releases/download/${VERSION}/lifecycled-linux-amd64
chmod +x /usr/bin/lifecycled
```

Available binaries:
- `lifecycled-linux-amd64`
- `lifecycled-linux-386`
- `lifecycled-linux-arm64`
- `lifecycled-linux-aarch64`
- `lifecycled-freebsd-amd64`
- `lifecycled-windows-amd64`

### Install from Source

```bash
go install github.com/buildkite/lifecycled/cmd/lifecycled@latest
```

## Configuration

Lifecycled is configured via command-line flags or environment variables (with `LIFECYCLED_` prefix).

### Required Configuration

| Flag | Environment Variable | Description |
|------|---------------------|-------------|
| `--handler` | `LIFECYCLED_HANDLER` | Path to the script to execute when a termination event occurs |

### Optional Configuration

| Flag | Environment Variable | Default | Description |
|------|---------------------|---------|-------------|
| `--instance-id` | `LIFECYCLED_INSTANCE_ID` | Auto-detected | EC2 instance ID to monitor |
| `--sns-topic` | `LIFECYCLED_SNS_TOPIC` | - | SNS topic ARN that receives lifecycle events |
| `--no-spot` | `LIFECYCLED_NO_SPOT` | `false` | Disable spot instance termination listener |
| `--json` | `LIFECYCLED_JSON` | `false` | Enable JSON logging format |
| `--debug` | `LIFECYCLED_DEBUG` | `false` | Enable debug logging |
| `--cloudwatch-group` | `LIFECYCLED_CLOUDWATCH_GROUP` | - | CloudWatch Logs group name |
| `--cloudwatch-stream` | `LIFECYCLED_CLOUDWATCH_STREAM` | Instance ID | CloudWatch Logs stream name |
| `--tags` | `LIFECYCLED_TAGS` | - | Comma-separated tags for SQS queues (e.g., `Team=platform,Environment=prod`) |
| `--spot-listener-interval` | `LIFECYCLED_SPOT_LISTENER_INTERVAL` | `5s` | Interval to check for spot termination notices |
| `--autoscaling-heartbeat-interval` | `LIFECYCLED_AUTOSCALING_HEARTBEAT_INTERVAL` | `10s` | Interval to send lifecycle heartbeats to AWS; keep shorter than the hook's `HeartbeatTimeout` |

### AWS Configuration

Lifecycled requires AWS credentials and region configuration:

| Environment Variable | Description |
|---------------------|-------------|
| `AWS_REGION` | AWS region (see resolution order below) |
| `AWS_DEFAULT_REGION` | Fallback region used when `AWS_REGION` is unset |
| `AWS_ACCESS_KEY_ID` | AWS access key (optional if using IAM instance profile) |
| `AWS_SECRET_ACCESS_KEY` | AWS secret key (optional if using IAM instance profile) |

The region is resolved in order: `AWS_REGION`, then `AWS_DEFAULT_REGION`, then the active profile's region, and finally the EC2 instance metadata service when running on EC2. Lifecycled exits at startup if none of these supply a region.

## Usage

### Systemd Installation

1. **Install the binary:**
   ```bash
   curl -Lf -o /usr/bin/lifecycled \
     https://github.com/buildkite/lifecycled/releases/download/${VERSION}/lifecycled-linux-amd64
   chmod +x /usr/bin/lifecycled
   ```

2. **Install the systemd service:**
   ```bash
   curl -Lf -o /etc/systemd/system/lifecycled.service \
     https://raw.githubusercontent.com/buildkite/lifecycled/${VERSION}/init/systemd/lifecycled.unit
   ```

3. **Configure lifecycled:**

   Create `/etc/lifecycled` with your configuration:
   ```bash
   LIFECYCLED_HANDLER=/usr/local/bin/my_shutdown_handler.sh
   LIFECYCLED_SNS_TOPIC=arn:aws:sns:us-east-1:123456789012:my-lifecycle-topic
   LIFECYCLED_CLOUDWATCH_GROUP=/aws/lifecycled
   AWS_REGION=us-east-1
   ```

4. **Start the service:**
   ```bash
   systemctl daemon-reload
   systemctl enable lifecycled
   systemctl start lifecycled
   systemctl status lifecycled
   ```

### Manual Execution

```bash
lifecycled \
  --handler=/usr/local/bin/shutdown.sh \
  --sns-topic=arn:aws:sns:us-east-1:123456789012:lifecycle-topic \
  --cloudwatch-group=/aws/lifecycled \
  --tags="Team=platform,Environment=prod" \
  --debug
```

### Docker

```dockerfile
FROM ubuntu:20.04

# Install lifecycled
RUN curl -Lf -o /usr/bin/lifecycled \
  https://github.com/buildkite/lifecycled/releases/download/v3.3.0/lifecycled-linux-amd64 && \
  chmod +x /usr/bin/lifecycled

# Copy your handler script
COPY shutdown-handler.sh /usr/local/bin/shutdown-handler.sh
RUN chmod +x /usr/local/bin/shutdown-handler.sh

# Run lifecycled
CMD ["/usr/bin/lifecycled", "--handler=/usr/local/bin/shutdown-handler.sh"]
```

## Handler Scripts

Handler scripts receive termination events and perform graceful shutdown operations. The script is passed the event type and instance ID as arguments.

### Arguments Passed to Handler

- **AutoScaling Events**: `autoscaling:EC2_INSTANCE_TERMINATING i-001405f0fc67e3b12`
- **Spot Termination Events**: `ec2:SPOT_INSTANCE_TERMINATION i-001405f0fc67e3b12 2015-01-05T18:02:00Z`

### Example Handler Script

```bash
#!/bin/bash
set -euo pipefail

EVENT_TYPE="$1"
INSTANCE_ID="$2"

echo "Received termination event: ${EVENT_TYPE} for instance ${INSTANCE_ID}"

# Function to wait for service shutdown
await_shutdown() {
  local service=$1
  echo -n "Waiting for ${service} to stop..."
  while systemctl is-active "${service}" > /dev/null 2>&1; do
    sleep 1
  done
  echo "Done!"
}

# Gracefully stop services
systemctl stop myapp.service
await_shutdown myapp.service

# Drain connections, flush logs, etc.
echo "Flushing logs..."
journalctl --sync

echo "Graceful shutdown complete"
```

### Handler Script Best Practices

1. **Use proper error handling**: Set `set -euo pipefail` to catch errors
2. **Keep it fast**: Handler execution time is limited by the lifecycle hook timeout (default 60 seconds)
3. **Log important actions**: Output will be captured by systemd or CloudWatch
4. **Test thoroughly**: Test your handler script independently before deploying
5. **Handle both event types**: Check `$1` if you need different behavior for spot vs autoscaling events

### Example: Draining a Load Balancer

```bash
#!/bin/bash
set -euo pipefail

# Deregister from load balancer and wait for connection draining
aws elbv2 deregister-targets \
  --target-group-arn "${TARGET_GROUP_ARN}" \
  --targets Id="${INSTANCE_ID}"

# Wait for draining
sleep 30

# Stop application
systemctl stop myapp.service
```

### Example: Kubernetes Node Drain

```bash
#!/bin/bash
set -euo pipefail

# Cordon and drain the node
kubectl cordon "${NODE_NAME}"
kubectl drain "${NODE_NAME}" \
  --ignore-daemonsets \
  --delete-emptydir-data \
  --force \
  --grace-period=300
```

## IAM Permissions

Lifecycled requires specific IAM permissions to function properly.

### EC2 Instance Role Permissions

Attach these permissions to your EC2 instance IAM role:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "autoscaling:RecordLifecycleActionHeartbeat",
        "autoscaling:CompleteLifecycleAction"
      ],
      "Resource": "*"
    },
    {
      "Effect": "Allow",
      "Action": [
        "sns:Subscribe",
        "sns:Unsubscribe"
      ],
      "Resource": "arn:aws:sns:REGION:ACCOUNT:your-lifecycle-topic"
    },
    {
      "Effect": "Allow",
      "Action": [
        "sqs:CreateQueue",
        "sqs:DeleteQueue",
        "sqs:ReceiveMessage",
        "sqs:DeleteMessage",
        "sqs:GetQueueUrl",
        "sqs:GetQueueAttributes",
        "sqs:SetQueueAttributes",
        "sqs:TagQueue"
      ],
      "Resource": "arn:aws:sqs:REGION:ACCOUNT:lifecycled-*"
    }
  ]
}
```

### Optional: CloudWatch Logs Permissions

If using `--cloudwatch-group`:

```json
{
  "Effect": "Allow",
  "Action": [
    "logs:CreateLogGroup",
    "logs:CreateLogStream",
    "logs:PutLogEvents"
  ],
  "Resource": "arn:aws:logs:REGION:ACCOUNT:log-group:YOUR_LOG_GROUP:*"
}
```

`logs:CreateLogGroup` is optional: lifecycled attempts to create the log group at startup but treats an access-denied response as a signal that the group is managed elsewhere and carries on. Omit it if the group is provisioned out of band; `logs:CreateLogStream` and `logs:PutLogEvents` are always required.

Log lines are delivered synchronously, one `PutLogEvents` call per line, so each line reaches CloudWatch before the daemon continues. This keeps lines from being dropped when an instance terminates mid-drain, but every line is a separate network round-trip bounded to five seconds. Leaving `--debug` on in production is chatty and will slow the daemon whenever CloudWatch is slow to respond.

### AutoScaling Lifecycle Hook Role

The lifecycle hook itself needs permissions to publish to SNS:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": "sns:Publish",
      "Resource": "arn:aws:sns:REGION:ACCOUNT:your-lifecycle-topic"
    }
  ]
}
```

## AWS Setup

### Creating an SNS Topic

```bash
aws sns create-topic --name my-lifecycle-topic --region us-east-1
```

### Creating a Lifecycle Hook

```bash
aws autoscaling put-lifecycle-hook \
  --lifecycle-hook-name my-termination-hook \
  --auto-scaling-group-name my-asg \
  --lifecycle-transition autoscaling:EC2_INSTANCE_TERMINATING \
  --notification-target-arn arn:aws:sns:us-east-1:123456789012:my-lifecycle-topic \
  --role-arn arn:aws:iam::123456789012:role/lifecycle-hook-role \
  --heartbeat-timeout 300 \
  --default-result CONTINUE
```

### Terraform Example

See the [terraform/](terraform/) directory for a complete Terraform example that sets up:
- AutoScaling Group with lifecycle hooks
- SNS topic and subscriptions
- SQS queues
- IAM roles and policies
- CloudWatch log groups

## Cleaning Up Orphaned Queues and Subscriptions

For every instance it runs on, lifecycled creates an SQS queue and an SNS subscription named with a `lifecycled-` prefix. These are removed when an instance shuts down cleanly, but an ungraceful termination can leave them behind, and over time the orphans accumulate against your account's SQS and SNS limits.

The [`lifecycled-queue-cleaner`](tools/lifecycled-queue-cleaner) tool removes them. It lists the running instances, then deletes the `lifecycled-` queues and subscriptions that no longer map to one. Because it is destructive, it logs the resolved region and account at startup and accepts an `-account` guard that aborts before any delete if the resolved account does not match.

```bash
cd tools/lifecycled-queue-cleaner
go run . -account 123456789012
```

See the [tool's README](tools/lifecycled-queue-cleaner/README.md) for how it resolves credentials and region (including AWS SSO) and the IAM permissions it needs.

## Troubleshooting

### Checking Service Status

```bash
systemctl status lifecycled
journalctl -u lifecycled -f
```

### Enable Debug Logging

Add to `/etc/lifecycled`:
```bash
LIFECYCLED_DEBUG=true
```

Then restart:
```bash
systemctl restart lifecycled
```

### Common Issues

**Problem**: "Failed to lookup instance id"
- **Solution**: Ensure the instance has access to EC2 metadata service or set `LIFECYCLED_INSTANCE_ID` explicitly

**Problem**: "ec2 metadata is not available" or the daemon exits immediately when not running on EC2
- **Solution**: The spot listener is enabled by default and probes the EC2 instance metadata service at startup. Off EC2, disable it with `--no-spot` (or `LIFECYCLED_NO_SPOT=true`)

**Problem**: "No region resolved" at startup
- **Solution**: Set `AWS_REGION` (or `AWS_DEFAULT_REGION`, or a profile region); the metadata fallback only applies on EC2

**Problem**: "Permission denied" errors with SQS/SNS
- **Solution**: Verify IAM instance profile has required permissions

**Problem**: Handler script not executing
- **Solution**: Check that the handler path is correct and the script is executable (`chmod +x`)

**Problem**: "Context deadline exceeded" during handler execution
- **Solution**: Increase the lifecycle hook heartbeat timeout or optimize your handler script

### Testing Handler Scripts Locally

You can test your handler script manually:

```bash
/usr/local/bin/shutdown-handler.sh "autoscaling:EC2_INSTANCE_TERMINATING" "i-1234567890abcdef0"
```

### Viewing CloudWatch Logs

If using CloudWatch logging:

```bash
aws logs tail /aws/lifecycled --follow --region us-east-1
```

## Development

For information on building, testing, and contributing to lifecycled, see [DEVELOPMENT.md](DEVELOPMENT.md).

## License

See [LICENSE](LICENSE) (MIT)
