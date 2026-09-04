locals {
  app   = "api"
  units = ["sandboxd-api.service", "sandboxd-tunnel.service"]
  settings = merge({
    domain        = var.hostname
    previewDomain = var.preview_domain
    admin         = { user = var.admin_user, sshKeys = var.ssh_public_keys }
    tailscale     = { enable = var.tailscale_host != "", host = var.tailscale_host }
  }, var.extra_settings)
  # Both units read one file: the API's secrets plus the tunnel token.
  env = merge(var.env, { TUNNEL_TOKEN = data.cloudflare_zero_trust_tunnel_cloudflared_token.this.token })
}

# Remotely managed tunnel: ingress lives here, the host only holds the connector token.
resource "cloudflare_zero_trust_tunnel_cloudflared" "this" {
  account_id = var.cloudflare_account_id
  name       = var.name
  config_src = "cloudflare"
}

resource "cloudflare_zero_trust_tunnel_cloudflared_config" "this" {
  account_id = var.cloudflare_account_id
  tunnel_id  = cloudflare_zero_trust_tunnel_cloudflared.this.id
  config = {
    ingress = [
      { hostname = var.hostname, service = "http://localhost:${var.tunnel_port}" },
      { hostname = "*.${var.preview_domain}", service = "http://localhost:${var.tunnel_port}" },
      { service = "http_status:404" },
    ]
  }
}

data "cloudflare_zero_trust_tunnel_cloudflared_token" "this" {
  account_id = var.cloudflare_account_id
  tunnel_id  = cloudflare_zero_trust_tunnel_cloudflared.this.id
}

resource "cloudflare_dns_record" "api" {
  zone_id = var.cloudflare_zone_id
  name    = var.hostname
  type    = "CNAME"
  content = "${cloudflare_zero_trust_tunnel_cloudflared.this.id}.cfargotunnel.com"
  proxied = true
  ttl     = 1
}

resource "cloudflare_dns_record" "preview" {
  zone_id = var.cloudflare_zone_id
  name    = "*.${var.preview_domain}"
  type    = "CNAME"
  content = "${cloudflare_zero_trust_tunnel_cloudflared.this.id}.cfargotunnel.com"
  proxied = true
  ttl     = 1
}
