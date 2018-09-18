# -------------------------------------------------------------------------------
# Resources
# -------------------------------------------------------------------------------
data "aws_region" "current" {}

data "aws_caller_identity" "current" {}

# Create an S3 bucket for uploading the artifact (pre-prefixed with the account ID to avoid conflicting bucket names)
resource "aws_s3_bucket" "artifact" {
  bucket = "${data.aws_caller_identity.current.account_id}-${var.name_prefix}-artifact"
  acl    = "private"

  tags = "${merge(var.tags, map("Name", "${data.aws_caller_identity.current.account_id}-${var.name_prefix}-artifact"))}"
}

# Upload the lifecycled artifact
resource "aws_s3_bucket_object" "artifact" {
  bucket = "${aws_s3_bucket.artifact.id}"
  key    = "lifecycled-linux-amd64"
  source = "${var.binary_path}"
  etag   = "${md5(file("${var.binary_path}"))}"
}

# Cloud init script for the autoscaling group
data "template_file" "main" {
  template = "${file("${path.module}/cloud-config.yml")}"

  vars {
    region          = "${data.aws_region.current.name}"
    stack_name      = "${var.name_prefix}-asg"
    log_group_name  = "${aws_cloudwatch_log_group.main.name}"
    lifecycle_topic = "${aws_sns_topic.main.arn}"
    artifact_bucket = "${aws_s3_bucket.artifact.id}"
    artifact_key    = "${aws_s3_bucket_object.artifact.id}"
    artifact_etag   = "${aws_s3_bucket_object.artifact.etag}"
  }
}

# The autoscaling group
module "asg" {
  source  = "telia-oss/asg/aws"
  version = "0.2.0"

  name_prefix       = "${var.name_prefix}"
  user_data         = "${data.template_file.main.rendered}"
  vpc_id            = "${var.vpc_id}"
  subnet_ids        = "${var.subnet_ids}"
  await_signal      = "true"
  pause_time        = "PT5M"
  health_check_type = "EC2"
  instance_policy   = "${data.aws_iam_policy_document.permissions.json}"
  min_size          = "${var.instance_count}"
  instance_type     = "${var.instance_type}"
  instance_ami      = "${var.instance_ami}"
  instance_key      = "${var.instance_key}"
  tags              = "${var.tags}"
}

# Allow SSH ingress if a EC2 key pair is specified.
resource "aws_security_group_rule" "ssh_ingress" {
  count             = "${var.instance_key != "" ? 1 : 0}"
  security_group_id = "${module.asg.security_group_id}"
  type              = "ingress"
  protocol          = "tcp"
  from_port         = 22
  to_port           = 22
  cidr_blocks       = ["0.0.0.0/0"]
}

# Create log group where we can write the daemon logs.
resource "aws_cloudwatch_log_group" "main" {
  name = "${var.name_prefix}-daemon"
}

# Instance profile for the autoscaling group.
data "aws_iam_policy_document" "permissions" {
  statement {
    effect = "Allow"

    actions = [
      "logs:CreateLogStream",
      "logs:PutLogEvents",
    ]

    resources = [
      "${aws_cloudwatch_log_group.main.arn}",
    ]
  }

  statement {
    effect = "Allow"

    actions = [
      "logs:DescribeLogStreams",
    ]

    resources = [
      "*",
    ]
  }

  statement {
    effect = "Allow"

    actions = [
      "sns:Subscribe",
      "sns:Unsubscribe",
    ]

    resources = [
      "${aws_sns_topic.main.arn}",
    ]
  }

  statement {
    effect = "Allow"

    actions = [
      "sqs:*",
    ]

    resources = ["arn:aws:sqs:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:lifecycled-*"]
  }

  statement {
    effect = "Allow"

    actions = [
      "s3:*",
    ]

    resources = [
      "${aws_s3_bucket.artifact.arn}/*",
      "${aws_s3_bucket.artifact.arn}",
    ]
  }

  statement {
    effect = "Allow"

    actions = [
      "autoscaling:RecordLifecycleActionHeartbeat",
      "autoscaling:CompleteLifecycleAction",
    ]

    resources = ["*"]
  }
}

# SNS topic for the lifecycle hook
resource "aws_sns_topic" "main" {
  name = "${var.name_prefix}-lifecycle"
}

# Lifecycle hook
resource "aws_autoscaling_lifecycle_hook" "main" {
  name                    = "${var.name_prefix}-lifecycle"
  autoscaling_group_name  = "${module.asg.id}"
  lifecycle_transition    = "autoscaling:EC2_INSTANCE_TERMINATING"
  default_result          = "CONTINUE"
  heartbeat_timeout       = "60"
  notification_target_arn = "${aws_sns_topic.main.arn}"
  role_arn                = "${aws_iam_role.lifecycle.arn}"
}

# Execution role and policies for the lifecycle hook
resource "aws_iam_role" "lifecycle" {
  name               = "${var.name_prefix}-lifecycle-role"
  assume_role_policy = "${data.aws_iam_policy_document.asg_assume.json}"
}

resource "aws_iam_role_policy" "lifecycle" {
  name   = "${var.name_prefix}-lifecycle-permissions"
  role   = "${aws_iam_role.lifecycle.id}"
  policy = "${data.aws_iam_policy_document.asg_permissions.json}"
}

data "aws_iam_policy_document" "asg_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["autoscaling.amazonaws.com"]
    }
  }
}

data "aws_iam_policy_document" "asg_permissions" {
  statement {
    effect = "Allow"

    resources = [
      "${aws_sns_topic.main.arn}",
    ]

    actions = [
      "sns:Publish",
    ]
  }
}
