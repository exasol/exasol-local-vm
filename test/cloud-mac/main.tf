terraform {
  required_version = ">= 1.5"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = var.region
}

resource "aws_vpc" "mac" {
  cidr_block           = "10.0.0.0/16"
  enable_dns_hostnames = true

  tags = { Name = "${var.name}-vpc" }
}

resource "aws_internet_gateway" "mac" {
  vpc_id = aws_vpc.mac.id

  tags = { Name = "${var.name}-igw" }
}

resource "aws_subnet" "mac" {
  vpc_id                  = aws_vpc.mac.id
  cidr_block              = "10.0.1.0/24"
  availability_zone       = "${var.region}${var.availability_zone_suffix}"
  map_public_ip_on_launch = true

  tags = { Name = "${var.name}-subnet" }
}

resource "aws_route_table" "mac" {
  vpc_id = aws_vpc.mac.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.mac.id
  }

  tags = { Name = "${var.name}-rt" }
}

resource "aws_route_table_association" "mac" {
  subnet_id      = aws_subnet.mac.id
  route_table_id = aws_route_table.mac.id
}

data "aws_ami" "macos" {
  most_recent = true
  owners      = ["amazon"]

  filter {
    name   = "name"
    values = [var.macos_ami_name_filter]
  }

  filter {
    name   = "architecture"
    values = [var.instance_type == "mac1.metal" ? "x86_64_mac" : "arm64_mac"]
  }
}

import {
  to = aws_key_pair.mac
  id = "${var.name}-key"
}

resource "aws_key_pair" "mac" {
  key_name   = "${var.name}-key"
  public_key = file(pathexpand(var.ssh_public_key_path))

  lifecycle {
    ignore_changes = [public_key]
  }
}

resource "aws_security_group" "mac" {
  name        = "${var.name}-sg"
  description = "Allow SSH to macOS test instance"
  vpc_id      = aws_vpc.mac.id

  ingress {
    description = "SSH"
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = var.ssh_allowed_cidrs
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

# Mac instances require a Dedicated Host
resource "aws_ec2_host" "mac" {
  instance_type     = var.instance_type
  availability_zone = "${var.region}${var.availability_zone_suffix}"
  auto_placement    = "off"
  host_recovery     = "off"

  tags = {
    Name = "${var.name}-host"
  }
}

resource "aws_instance" "mac" {
  ami               = data.aws_ami.macos.id
  instance_type     = var.instance_type
  host_id           = aws_ec2_host.mac.id
  subnet_id         = aws_subnet.mac.id
  key_name          = aws_key_pair.mac.key_name
  vpc_security_group_ids = [aws_security_group.mac.id]

  root_block_device {
    volume_size = var.root_volume_size_gb
    volume_type = "gp3"
  }

  tags = {
    Name = var.name
  }
}

output "public_ip" {
  description = "Public IP address of the cloud Mac instance"
  value       = aws_instance.mac.public_ip
}

output "ssh_key_path" {
  description = "Path to the SSH private key for connecting to the instance"
  value       = replace(pathexpand(var.ssh_public_key_path), ".pub", "")
}
