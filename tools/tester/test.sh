#!/bin/bash
set -euo pipefail

# This script expects the base stack to have been deployed
# It can be run locally, you can build a local binary with:
# GOOS=linux GOARCH=amd64 go build -a -tags netgo -ldflags '-w' -o lifecycled-linux-amd64 .

# parfait is a cloudformation tool that provides streaming output
if ! command -v parfait &> /dev/null ; then
  echo "~~~ Downloading parfait"
  wget -N https://github.com/lox/parfait/releases/download/v1.1.3/parfait_linux_amd64
  mv parfait_linux_amd64 parfait
  chmod +x ./parfait
  export PATH="$PATH:$PWD"
fi

# the s3 bucket to use for the stack
bucket=$(aws cloudformation describe-stacks \
  --stack-name lifecycled-test-base \
  --query 'Stacks[0].Outputs[?OutputKey==`S3Bucket`].OutputValue' \
  --output text)

# configurable stuff
stack_name="lifecycled-test-${BUILDKITE_JOB_ID:-dev-$(date +%s)}"
key_name="${KEY_NAME:-default}"
count="20"

# download the binary that was previously compiled
if [[ ! -f ./lifecycled-linux-amd64 ]] ; then
  echo "~~~ Downloading artifacts for binaries"
  buildkite-agent artifact download "lifecycled-linux-amd64" .
fi

bucket_path="$stack_name"
bucket_url="https://s3.amazonaws.com/${bucket}/${bucket_path}"

echo "~~~ Uploading artifacts to $bucket_url"
aws s3 sync ./init/ "s3://${bucket}/${bucket_path}/init" --acl public-read
aws s3 sync ./tools/tester/cloudformation/support/ "s3://${bucket}/${bucket_path}/support" --acl public-read
aws s3 cp ./lifecycled-linux-amd64 "s3://${bucket}/${bucket_path}/" --acl public-read

# test it can be downloaded
echo "~~~ Testing binary can be downloaded"
curl -Lf -I "$bucket_url/lifecycled-linux-amd64"

echo "~~~ Creating stack ${stack_name}"
parfait create-stack --no-rollback -t "./tools/tester/cloudformation/template.yml" "$stack_name" \
    "KeyName=${key_name}" \
    "DownloadURL=${bucket_url}" \
    "Count=${count}"

log_group=$(aws cloudformation describe-stacks \
  --stack-name "$stack_name" \
  --query 'Stacks[0].Outputs[?OutputKey==`LogGroup`].OutputValue' \
  --output text)

echo "Browse at https://console.aws.amazon.com/cloudwatch/home?region=us-east-1#logStream:group=${log_group};streamFilter=typeLogStreamPrefix"

auto_scaling_group=$(aws cloudformation describe-stacks \
  --stack-name "$stack_name" \
  --query 'Stacks[0].Outputs[?OutputKey==`AutoScaleGroup`].OutputValue' \
  --output text)

echo "~~~ Scaling down stack"
parfait update-stack "$stack_name" "Count=0"
