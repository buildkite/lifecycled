#!/bin/bash
set -euxo pipefail

# configurable stuff
stack_name="lifecycled-test-$(date +%s)"
key_name="lox"
vpc_id="$1"
spot_price="${2:-0}"

# lookup vpc config
subnets=$(aws ec2 describe-subnets --filters "Name=vpc-id,Values=$vpc_id" --query "Subnets[*].[SubnetId,AvailabilityZone]" --output text)
subnet_ids=$(awk '{print $1}' <<< "$subnets" | tr ' ' ',' | tr '\n' ',' | sed 's/,$//')

echo "Found vpc_id $vpc_id subnets $subnet_ids"

echo "--- Creating stack ${stack_name}"
aws cloudformation create-stack \
  --output text \
  --stack-name "$stack_name" \
  --disable-rollback \
  --parameters \
    "ParameterKey=KeyName,ParameterValue=${key_name}" \
    "ParameterKey=VpcId,ParameterValue=${vpc_id}" \
    "ParameterKey=Subnets,ParameterValue=\"${subnet_ids}\"" \
    "ParameterKey=SpotPrice,ParameterValue=${spot_price}" \
  --template-body "file://${PWD}/cloudformation/template.yml" \
  --capabilities CAPABILITY_IAM CAPABILITY_NAMED_IAM

echo "--- Waiting for stack to complete"
aws cloudformation wait stack-create-complete --stack-name "${stack_name}"
