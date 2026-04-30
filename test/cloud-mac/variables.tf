variable "region" {
  description = "AWS region (mac2.metal available in us-east-1, us-east-2, us-west-2, eu-west-1)"
  type        = string
  default     = "us-east-2"
}

variable "availability_zone_suffix" {
  description = "AZ suffix within the region (a, b, c). Dedicated hosts are AZ-specific."
  type        = string
  default     = "b"
}

variable "name" {
  description = "Name prefix for all resources"
  type        = string
  default     = "buildvm-mac-test"
}

variable "instance_type" {
  description = "EC2 Mac instance type: mac1.metal (Intel), mac2.metal (M1), mac2-m2.metal (M2)"
  type        = string
  default     = "mac2.metal"
}

variable "macos_ami_name_filter" {
  description = "Name glob to find the macOS AMI (amzn-ec2-macos-*)"
  type        = string
  default     = "amzn-ec2-macos-15*"
}

variable "ssh_public_key_path" {
  description = "Path to the SSH public key file to install on the instance"
  type        = string
  default     = "~/.ssh/id_ed25519.pub"
}

variable "ssh_allowed_cidrs" {
  description = "CIDR blocks allowed to SSH in. Defaults to your public IP — lock this down."
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

variable "root_volume_size_gb" {
  description = "Root EBS volume size in GB"
  type        = number
  default     = 100
}
