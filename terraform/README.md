# Terraform Example for Lifecycled

This directory contains a complete Terraform example that demonstrates how to set up an AWS AutoScaling Group with lifecycled for graceful instance termination.

## What Gets Created

This example provisions the following AWS resources:

### Core Infrastructure
- **AutoScaling Group**: Runs Amazon Linux 2 instances with lifecycled pre-installed
- **Launch Configuration**: Configures instances with cloud-init to set up lifecycled
- **Security Group**: Basic security group with optional SSH access

### Lifecycle Management
- **SNS Topic**: Receives lifecycle events from AutoScaling
- **Lifecycle Hook**: Triggers on `autoscaling:EC2_INSTANCE_TERMINATING` events
- **SQS Queues**: Created automatically by lifecycled to subscribe to the SNS topic

### Storage & Artifacts
- **S3 Bucket**: Stores the lifecycled binary for instance deployment
- **S3 Object**: The lifecycled binary uploaded from your build

### Logging
- **CloudWatch Log Group**: Collects lifecycled daemon logs from all instances

### IAM
- **EC2 Instance Role**: Permissions for lifecycled to function
- **Instance Profile**: Attaches the role to EC2 instances
- **Lifecycle Hook Role**: Allows AutoScaling to publish to SNS

## Prerequisites

### Required

1. **AWS Credentials**: Configured via AWS CLI or environment variables
2. **Terraform**: Version 1.0 or later
3. **Lifecycled Binary**: Build the binary first with `make release`
4. **VPC**: Default VPC must exist (or modify to use a custom VPC)

### Optional

- **EC2 Key Pair**: Create a key pair named `lifecycled-example` if you want SSH access to instances

To create a key pair:
```bash
aws ec2 create-key-pair \
  --key-name lifecycled-example \
  --query 'KeyMaterial' \
  --output text > ~/Downloads/lifecycled-example.pem
chmod 400 ~/Downloads/lifecycled-example.pem
```

## Usage

### Step 1: Build Lifecycled

From the repository root:

```bash
# Install gox if not already installed
go install github.com/mitchellh/gox@latest

# Build for Linux
make release
```

This creates `build/lifecycled-linux-amd64` which the Terraform configuration will upload to S3.

### Step 2: Configure Variables (Optional)

Edit [example.tf](./example.tf) to customize:

```hcl
module "example" {
  # ...

  instance_count = "1"          # Number of instances
  instance_type  = "t3.micro"   # Instance type
  instance_key   = "your-key"   # EC2 key pair name

  tags = {
    environment = "dev"
    terraform   = "True"
  }
}
```

### Step 3: Deploy

```bash
cd terraform/

# Initialize Terraform
terraform init

# Review the plan
terraform plan

# Apply the configuration
terraform apply
```

Type `yes` when prompted to create the resources.

### Step 4: Verify Deployment

Once deployed, check that lifecycled is running:

1. **Find the instance IP:**
   ```bash
   aws ec2 describe-instances \
     --filters "Name=tag:Name,Values=lifecycled-example*" \
     --query 'Reservations[0].Instances[0].PublicIpAddress' \
     --output text
   ```

2. **SSH into the instance:**
   ```bash
   ssh -i ~/Downloads/lifecycled-example.pem ec2-user@<instance-ip>
   ```

3. **Check lifecycled status:**
   ```bash
   sudo systemctl status lifecycled
   sudo journalctl -u lifecycled -f
   ```

4. **View CloudWatch logs:**
   ```bash
   aws logs tail /aws/lifecycled-example-daemon --follow
   ```

## Testing the Lifecycle Hook

To test that lifecycled properly handles termination events:

### Option 1: Scale Down the ASG

```bash
# Get the ASG name
ASG_NAME=$(aws autoscaling describe-auto-scaling-groups \
  --query 'AutoScalingGroups[?starts_with(AutoScalingGroupName, `lifecycled-example`)].AutoScalingGroupName' \
  --output text)

# Reduce desired capacity to 0
aws autoscaling set-desired-capacity \
  --auto-scaling-group-name "${ASG_NAME}" \
  --desired-capacity 0
```

This will trigger the lifecycle hook and you should see:
1. The lifecycle hook transitions the instance to "Terminating:Wait"
2. Lifecycled executes the handler script
3. Logs appear in CloudWatch showing the handler output
4. After the handler completes, the instance terminates

### Option 2: Manually Terminate an Instance

```bash
# Get instance ID
INSTANCE_ID=$(aws ec2 describe-instances \
  --filters "Name=tag:Name,Values=lifecycled-example*" \
  "Name=instance-state-name,Values=running" \
  --query 'Reservations[0].Instances[0].InstanceId' \
  --output text)

# Terminate the instance
aws ec2 terminate-instances --instance-ids "${INSTANCE_ID}"
```

### Expected Behavior

When a termination event occurs:

1. **Handler Execution** (from cloud-config.yml):
   ```
   hello from the handler
   [waits 120 seconds]
   goodbye from the handler
   ```

2. **Lifecycled Logs** (in CloudWatch):
   - "Received termination event"
   - "Executing handler"
   - Handler output
   - "Handler finished successfully"
   - "Completing lifecycle action"

3. **Instance Termination**: After the handler completes, the instance terminates gracefully

## Example Handler Script

The example uses a simple handler script (configured in cloud-config.yml):

```bash
#!/usr/bin/bash
set -euo pipefail

echo "hello from the handler"
sleep 120
echo "goodbye from the handler"
```

### Customizing the Handler

To use a more realistic handler, modify the `cloud-config.yml` in `modules/example/`:

```yaml
- path: "/usr/local/scripts/lifecycle-handler.sh"
  permissions: "0744"
  owner: "root"
  content: |
    #!/usr/bin/bash
    set -euo pipefail

    # Stop application gracefully
    systemctl stop myapp.service

    # Wait for connections to drain
    sleep 30

    # Cleanup tasks
    echo "Cleanup complete"
```

## Configuration Details

### Lifecycled Service Configuration

The systemd service is configured with:
- `--no-spot`: Disables spot termination listener (only handles AutoScaling events)
- `--tags`: Applies tags to SQS queues for cost tracking
- `--cloudwatch-group`: Sends logs to CloudWatch
- `--json`: JSON formatted logs
- `--debug`: Debug level logging

### IAM Permissions

The instance role includes permissions for:
- **CloudWatch Logs**: CreateLogStream, PutLogEvents, DescribeLogStreams
- **SNS**: Subscribe, Unsubscribe to the lifecycle topic
- **SQS**: Full access to `lifecycled-*` queues
- **S3**: Access to the artifact bucket
- **AutoScaling**: RecordLifecycleActionHeartbeat, CompleteLifecycleAction

### Lifecycle Hook Configuration

- **Default Result**: `CONTINUE` (proceed with termination if handler fails)
- **Heartbeat Timeout**: 60 seconds
- **Transition**: `autoscaling:EC2_INSTANCE_TERMINATING`

## Cleanup

To destroy all resources:

```bash
terraform destroy
```

Type `yes` when prompted. This will remove:
- All EC2 instances
- AutoScaling Group and Launch Configuration
- SNS topics and SQS queues
- S3 bucket and objects
- CloudWatch log groups
- IAM roles and policies
- Security groups

## Troubleshooting

### Issue: Binary not found on instance

**Symptom**: Instance starts but lifecycled service fails to start

**Solution**:
1. Verify the binary was built: `ls -lh build/lifecycled-linux-amd64`
2. Check S3 bucket contents: `aws s3 ls s3://<account-id>-lifecycled-example-artifact/`
3. SSH to instance and check: `ls -lh /usr/local/bin/lifecycled`

### Issue: Handler not executing

**Symptom**: Instance terminates immediately without running handler

**Solution**:
1. Check CloudWatch Logs for lifecycled daemon logs
2. Verify handler script exists and is executable: `ls -lh /usr/local/scripts/lifecycle-handler.sh`
3. Check systemd service logs: `sudo journalctl -u lifecycled -n 100`

### Issue: Instances may not pick up binary updates immediately

**Note**: When you update the lifecycled binary and run `terraform apply`:
1. The S3 object is updated with the new binary
2. Existing instances continue running with the old binary
3. New instances launched by the ASG will get the new binary via cloud-init

To update existing instances:
- Terminate them manually, or
- Scale the ASG to 0 then back to desired capacity, or
- Use instance refresh (AWS feature) if available

This is expected behavior - existing EC2 instances don't automatically re-run cloud-init when S3 objects change.

### Issue: Permission denied errors

**Solution**: Verify IAM instance profile is attached:
```bash
aws ec2 describe-instances \
  --instance-ids <instance-id> \
  --query 'Reservations[0].Instances[0].IamInstanceProfile'
```

### Issue: Lifecycle hook not triggering

**Solution**:
1. Verify SNS topic ARN is correct in the configuration
2. Check lifecycle hook exists:
   ```bash
   aws autoscaling describe-lifecycle-hooks \
     --auto-scaling-group-name <asg-name>
   ```
3. Verify lifecycle hook role has SNS publish permissions

## Next Steps

- Review the IAM permissions in `modules/example/main.tf` and adjust for your security requirements
- Customize the handler script for your specific shutdown requirements
- Consider adding health checks or custom metrics
- Review the [main README](../README.md) for more configuration options
- Explore the full IAM permission requirements in the main documentation

## Additional Resources

- [Main README](../README.md): Comprehensive lifecycled documentation
- [AWS AutoScaling Lifecycle Hooks Documentation](https://docs.aws.amazon.com/autoscaling/ec2/userguide/lifecycle-hooks.html)
- [Terraform AWS Provider Documentation](https://registry.terraform.io/providers/hashicorp/aws/latest/docs)
