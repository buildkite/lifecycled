#!/bin/bash
set -euxo pipefail

parfait() {
  docker run -t -e AWS_REGION -v "$PWD":/app -w "/app" --rm "lox24/parfait" "$@"
}

# configurable stuff
stack_name="lifecycled-test-$(date +%s)"
key_name="lox"
vpc_id="vpc-412cf53a"
spot_price="${2:-0}"

# setup binary file in s3
echo "~~~ Uploading artifacts to s3"
buildkite-agent artifact download "lifecycled-linux-amd64" .

bucket="buildkite-lifecycled-builds"
bucket_path="${BUILDKITE_JOB_ID}"
bucket_url="https://s3.amazonaws.com/${bucket}/${bucket_path}"

aws s3 sync ./init/ "s3://${bucket}/${bucket_path}/init" --acl public-read
aws s3 cp ./lifecycled-linux-amd64 "s3://${bucket}/${bucket_path}/" --acl public-read

# test it can be downloaded
curl -Lf -I "$bucket_url/lifecycled-linux-amd64"

# lookup vpc config
subnets=$(aws ec2 describe-subnets --filters "Name=vpc-id,Values=$vpc_id" --query "Subnets[*].[SubnetId,AvailabilityZone]" --output text)
subnet_ids=$(awk '{print $1}' <<< "$subnets" | tr ' ' ',' | tr '\n' ',' | sed 's/,$//')

echo "+++ Creating stack ${stack_name}"
parfait create-stack -t "./.buildkite/cloudformation/template.yml" "$stack_name" \
    "KeyName=${key_name}" \
    "VpcId=${vpc_id}" \
    "Subnets=${subnet_ids}" \
    "SpotPrice=${spot_price}" \
    "LifecycledDownloadURL=${bucket_url}"
