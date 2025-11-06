# -------------------------------------------------------------------------------
# Outputs
# -------------------------------------------------------------------------------

output "autoscaling_group_name" {
  description = "Name of the AutoScaling Group"
  value       = aws_autoscaling_group.main.name
}

output "sns_topic_arn" {
  description = "ARN of the SNS topic for lifecycle events"
  value       = aws_sns_topic.main.arn
}

output "cloudwatch_log_group" {
  description = "CloudWatch Log Group name for lifecycled logs"
  value       = aws_cloudwatch_log_group.main.name
}

output "s3_bucket_name" {
  description = "S3 bucket containing the lifecycled binary"
  value       = aws_s3_bucket.artifact.id
}

output "instance_role_name" {
  description = "Name of the IAM role attached to instances"
  value       = aws_iam_role.ec2.name
}

output "security_group_id" {
  description = "ID of the security group for instances"
  value       = aws_security_group.main.id
}
