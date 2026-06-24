# lifecycled-queue-cleaner

An operator tool that deletes orphaned `lifecycled-` SQS queues and their SNS
subscriptions left behind by terminated EC2 instances. It lists running
instances, then removes the queues and subscriptions that no longer map to one.

This is a destructive tool. Confirm the resolved region (logged at startup as
`Using region ...`) before letting it run, and make sure the credentials in use
point at the account you intend to clean.

## Usage

```bash
go run . [-parallel N]
```

`-parallel` controls how many queue deletes run concurrently (default 20).

## AWS credentials and region

The tool builds its AWS session with shared configuration enabled, so it
resolves credentials and region the same way the AWS CLI does. In precedence
order, region comes from the `AWS_REGION` environment variable, then the active
profile in `~/.aws/config`, and finally the EC2 instance metadata service when
nothing else supplies one. If you run the tool off an EC2 instance with no
region configured, set `AWS_REGION` (or a profile region) rather than relying on
the metadata fallback.

Because shared configuration is enabled, named profiles work via `AWS_PROFILE`,
including AWS SSO profiles. To use SSO, log in first and select the profile:

```bash
aws sso login --profile my-profile
AWS_PROFILE=my-profile go run .
```
