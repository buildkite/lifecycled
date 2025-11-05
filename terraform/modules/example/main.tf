# -------------------------------------------------------------------------------
# Resources
# -------------------------------------------------------------------------------
data "aws_region" "current" {}

data "aws_caller_identity" "current" {}

locals {
  formatted_tags = join(",", [for k, v in var.tags : "${k}=${v}"])

  # Cloud init script for the autoscaling group
  # Using templatefile() instead of deprecated template_file data source
  user_data = templatefile("${path.module}/cloud-config.yml", {
    region          = data.aws_region.current.name
    stack_name      = "${var.name_prefix}-asg"
    log_group_name  = aws_cloudwatch_log_group.main.name
    lifecycle_topic = aws_sns_topic.main.arn
    artifact_bucket = aws_s3_bucket.artifact.id
    artifact_key    = aws_s3_object.artifact.id
    artifact_etag   = aws_s3_object.artifact.etag
    tags            = local.formatted_tags
  })
}

# Create an S3 bucket for uploading the artifact (pre-prefixed with the account ID to avoid conflicting bucket names)
resource "aws_s3_bucket" "artifact" {
  bucket = "${data.aws_caller_identity.current.account_id}-${var.name_prefix}-artifact"

  tags = merge(var.tags, {
    Name = "${data.aws_caller_identity.current.account_id}-${var.name_prefix}-artifact"
  })
}

resource "aws_s3_bucket_acl" "artifact" {
  bucket = aws_s3_bucket.artifact.id
  acl    = "private"
}

# Upload the lifecycled artifact
resource "aws_s3_object" "artifact" {
  bucket = aws_s3_bucket.artifact.id
  key    = "lifecycled-linux-amd64"
  source = var.binary_path
  etag   = filemd5(var.binary_path)
}

resource "aws_launch_configuration" "main" {
  name_prefix          = var.name_prefix
  image_id             = var.instance_ami
  instance_type        = var.instance_type
  key_name             = var.instance_key
  iam_instance_profile = aws_iam_instance_profile.ec2.name
  security_groups      = [aws_security_group.main.id]

  user_data = local.user_data

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_autoscaling_group" "main" {
  name                 = "${var.name_prefix}-${aws_launch_configuration.main.name}"
  launch_configuration = aws_launch_configuration.main.id
  vpc_zone_identifier  = var.subnet_ids

  min_size         = 0
  desired_capacity = var.instance_count
  max_size         = 1

  lifecycle {
    create_before_destroy = true
  }

  initial_lifecycle_hook {
    name                    = "${var.name_prefix}-lifecycle"
    default_result          = "CONTINUE"
    heartbeat_timeout       = 60
    lifecycle_transition    = "autoscaling:EC2_INSTANCE_TERMINATING"
    notification_target_arn = aws_sns_topic.main.arn
    role_arn                = aws_iam_role.lifecycle_hook.arn
  }

  tag {
    key                 = "Name"
    value               = var.name_prefix
    propagate_at_launch = true
  }

  dynamic "tag" {
    for_each = var.tags
    content {
      key                 = tag.key
      value               = tag.value
      propagate_at_launch = true
    }
  }
}

resource "aws_security_group" "main" {
  name        = "${var.name_prefix}-sg"
  description = "Allow access to lifecycled instances"
  vpc_id      = var.vpc_id

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-sg"
  })
}

# Allow SSH ingress if a EC2 key pair is specified.
resource "aws_security_group_rule" "ssh_ingress" {
  count             = var.instance_key != "" ? 1 : 0
  security_group_id = aws_security_group.main.id
  type              = "ingress"
  protocol          = "tcp"
  from_port         = 22
  to_port           = 22
  cidr_blocks       = ["0.0.0.0/0"]
}

# Create log group where we can write the daemon logs.
resource "aws_cloudwatch_log_group" "main" {
  name = "${var.name_prefix}-daemon"

  tags = var.tags
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
      aws_cloudwatch_log_group.main.arn,
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
      aws_sns_topic.main.arn,
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
      aws_s3_bucket.artifact.arn,
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

resource "aws_iam_instance_profile" "ec2" {
  name = "${var.name_prefix}-ec2-instance-profile"
  role = aws_iam_role.ec2.name

  tags = var.tags
}

resource "aws_iam_role" "ec2" {
  name               = "${var.name_prefix}-ec2-role"
  assume_role_policy = data.aws_iam_policy_document.ec2_assume.json

  tags = var.tags
}

resource "aws_iam_role_policy" "ec2" {
  name   = "${var.name_prefix}-ec2-permissions"
  role   = aws_iam_role.ec2.id
  policy = data.aws_iam_policy_document.permissions.json
}

data "aws_iam_policy_document" "ec2_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

# SNS topic for the lifecycle hook
resource "aws_sns_topic" "main" {
  name = "${var.name_prefix}-lifecycle"

  tags = var.tags
}

# Execution role and policies for the lifecycle hook
resource "aws_iam_role" "lifecycle_hook" {
  name               = "${var.name_prefix}-lifecycle-role"
  assume_role_policy = data.aws_iam_policy_document.asg_assume.json

  tags = var.tags
}

resource "aws_iam_role_policy" "lifecycle_hook" {
  name   = "${var.name_prefix}-lifecycle-asg-permissions"
  role   = aws_iam_role.lifecycle_hook.id
  policy = data.aws_iam_policy_document.asg_permissions.json
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
      aws_sns_topic.main.arn,
    ]

    actions = [
      "sns:Publish",
    ]
  }
}
