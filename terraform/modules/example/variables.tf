# ------------------------------------------------------------------------------
# Variables
# ------------------------------------------------------------------------------
variable "name_prefix" {
  description = "Prefix used for resource names."
  type        = string
}

variable "binary_path" {
  description = "Path to a linux binary of lifecycled which will be installed on the instance."
  type        = string
  default     = "../lifecycled-linux-amd64"
}

variable "instance_key" {
  description = "Name of an EC2 key pair which will be allowed to SSH to the instance."
  type        = string
  default     = ""
}

variable "vpc_id" {
  description = "ID of the VPC for the subnets."
  type        = string
}

variable "subnet_ids" {
  description = "IDs of subnets where the instances will be provisioned."
  type        = list(string)
}

variable "instance_count" {
  description = "Desired (and minimum) number of instances."
  type        = number
  default     = 1
}

variable "instance_ami" {
  description = "ID of an Amazon Linux 2 AMI. (Comes with SSM agent installed)"
  type        = string
  default     = "ami-db51c2a2"
}

variable "instance_type" {
  description = "Type of instance to provision."
  type        = string
  default     = "t3.micro"
}

variable "tags" {
  description = "A map of tags (key-value pairs) passed to resources."
  type        = map(string)
  default     = {}
}
