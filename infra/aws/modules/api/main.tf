locals {
  app        = "api"
  app_config = "aws-api"
  units      = ["sandboxd-api.service", "traefik.service"]
  settings = merge({
    domain        = var.hostname
    previewDomain = var.preview_domain
    acmeEmail     = var.acme_email
    admin         = { user = var.admin_user, sshKeys = var.ssh_public_keys }
    tailscale     = { enable = var.tailscale_host != "", host = var.tailscale_host }
  }, var.extra_settings)
  env = var.env
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

resource "cloudflare_dns_record" "api" {
  zone_id = var.cloudflare_zone_id
  name    = var.hostname
  type    = "A"
  content = aws_eip.this.public_ip
  proxied = false
  ttl     = 300
}

resource "cloudflare_dns_record" "preview" {
  zone_id = var.cloudflare_zone_id
  name    = "*.${var.preview_domain}"
  type    = "A"
  content = aws_eip.this.public_ip
  proxied = false
  ttl     = 300
}

# traefik reads CF_DNS_API_TOKEN from its own env file (services.sandboxd.ingress.acmeEnvironmentFile).
resource "terraform_data" "traefik_env" {
  triggers_replace = [sha256(var.cloudflare_dns_api_token), aws_instance.this.id]

  connection {
    type = "ssh"
    host = aws_eip.this.public_ip
    user = "root"
  }

  provisioner "file" {
    content     = "CF_DNS_API_TOKEN=\"${var.cloudflare_dns_api_token}\"\n"
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
