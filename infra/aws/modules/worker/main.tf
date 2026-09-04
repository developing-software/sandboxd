locals {
  app        = "worker"
  app_config = "aws-worker"
  units      = ["sandboxd-worker.service"]
  settings = merge({
    domain        = var.api_hostname
    previewDomain = var.api_hostname
    admin         = { user = var.admin_user, sshKeys = var.ssh_public_keys }
    tailscale     = { enable = var.tailscale_host != "", host = var.tailscale_host }
  }, var.extra_settings)
  env = var.env
}

resource "aws_security_group" "this" {
  name        = var.name
  description = "sandboxd worker: ssh only, the tunnel to the control plane is outbound"
  vpc_id      = local.vpc_id
  tags        = var.tags

  ingress {
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = var.allowed_ssh_cidrs
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}
