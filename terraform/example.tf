terraform {
  required_version = "0.11.8"
}

# Use the default VPC and subnets
data "aws_vpc" "main" {
  default = true
}

data "aws_subnet_ids" "main" {
  vpc_id = "${data.aws_vpc.main.id}"
}

# Use the latest Amazon Linux 2 AMI
data "aws_ami" "linux2" {
  owners      = ["amazon"]
  most_recent = true

  filter {
    name   = "virtualization-type"
    values = ["hvm"]
  }

  filter {
    name   = "architecture"
    values = ["x86_64"]
  }

  filter {
    name   = "root-device-type"
    values = ["ebs"]
  }

  filter {
    name   = "name"
    values = ["amzn2-ami*gp2"]
  }
}

variable "instance_key" {
  description = "Name of EC2 Keypair"
  default = "lifecycled-example"
}

module "example" {
  source = "modules/example"

  name_prefix = "lifecycled-example"
  vpc_id      = "${data.aws_vpc.main.id}"
  subnet_ids  = ["${data.aws_subnet_ids.main.ids}"]

  instance_ami   = "${data.aws_ami.linux2.id}"
  instance_count = "1"
  instance_type  = "t3.micro"
  instance_key   = "${var.instance_key}"

  binary_path = "../build/lifecycled-linux-amd64"

  tags = {
    environment = "dev"
    terraform   = "True"
  }
}
