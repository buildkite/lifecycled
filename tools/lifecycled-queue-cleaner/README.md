# lifecycled-queue-cleaner

An operator tool that deletes orphaned `lifecycled-` SQS queues and their SNS
subscriptions left behind by terminated EC2 instances. It lists running
instances, then removes the queues and subscriptions that no longer map to one.

This is a destructive tool. It logs the resolved region and account at startup
(`Using region ...` and `Using account ...`); confirm both point at what you
intend to clean before letting it run.

## Usage

```bash
go run . [-parallel N]
```

`-parallel` controls how many queue deletes run concurrently (default 20).

## AWS credentials and region

The tool builds its AWS session with shared configuration enabled, so it
resolves credentials and region the same way the AWS CLI does. In precedence
order, region comes from `AWS_REGION`, then `AWS_DEFAULT_REGION`, then the region
of the active profile in `~/.aws/config` (selected by `AWS_PROFILE`, or the
`default` profile), and finally the EC2 instance metadata service when nothing
else supplies one. If you run the tool off an EC2 instance with no region
configured, set `AWS_REGION` or `AWS_DEFAULT_REGION` (or a profile region) rather
than relying on the metadata fallback.

Because shared configuration is enabled, named profiles work via `AWS_PROFILE`,
including AWS SSO profiles. To use SSO, log in first and select the profile:

```bash
aws sso login --profile my-profile
AWS_PROFILE=my-profile go run .
```

SSO sessions expire and are not refreshed mid-run, so a long cleanup can exit
with an authentication error partway through. The tool re-lists queues and
subscriptions on each run, so it is safe to run again after `aws sso login` and
it picks up wherever the previous run left off.

## Required IAM permissions

The credentials need:

- `ec2:DescribeInstances`
- `sqs:ListQueues` and `sqs:DeleteQueue`
- `sns:ListSubscriptions`, `sns:GetTopicAttributes`, and `sns:Unsubscribe`

`sts:GetCallerIdentity`, used for the startup account check, requires no
permission. Missing any of the above surfaces as a fatal error partway through a
run, so confirm the policy before a large cleanup.
