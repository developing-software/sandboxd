locals {
  app        = "api"
  app_config = "aws-api"
  units      = ["sandboxd-api.service", "traefik.service"]
  settings = merge({
    domain          = var.hostname
    previewDomain   = var.preview_domain
    acmeEmail       = var.acme_email
    acmeDnsProvider = var.acme_dns_provider
    admin           = { user = var.admin_user, sshKeys = var.ssh_public_keys }
    tailscale       = { enable = var.tailscale_host != "", host = var.tailscale_host }
  }, var.extra_settings)
  env       = var.env
  acme_file = join("\n", concat([for k, v in var.acme_env : "${k}=\"${replace(v, "\"", "\\\"")}\""], [""]))
}

resource "aws_security_group" "this" {
  name        = var.name
  description = "sandboxd control plane: ssh, http, https"
  vpc_id      = local.vpc_id
  tags        = var.tags

  ingress {
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = var.allowed_ssh_cidrs
  }
  ingress {
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }
  ingress {
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

# traefik reads the DNS provider's credentials from its own env file
# (services.sandboxd.ingress.acmeEnvironmentFile).
resource "terraform_data" "traefik_env" {
  triggers_replace = [sha256(local.acme_file), aws_instance.this.id]

  connection {
    type = "ssh"
    host = aws_eip.this.public_ip
    user = "root"
  }

  provisioner "file" {
    content     = local.acme_file
    destination = "/tmp/sandboxd-traefik.env"
  }

  provisioner "remote-exec" {
    inline = [
      "install -d -m 0755 /etc/sandboxd",
      "install -m 0600 -o root -g root /tmp/sandboxd-traefik.env /etc/sandboxd/traefik.env",
      "rm -f /tmp/sandboxd-traefik.env",
    ]
  }
}
