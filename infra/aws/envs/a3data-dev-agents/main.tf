# One control plane with a public IP and one worker on the official NixOS AMI, DNS in
# Cloudflare. The modules know nothing about DNS: a Route53 env swaps the two records
# below for aws_route53_record and sets acme_dns_provider = "route53".
locals {
  flake = "../../../.."
  api_env = merge(
    { SANDBOXD_SERVICE_TOKEN = var.service_token },
    var.llm_base_url == null ? {} : { SANDBOXD_LLM_BASE_URL = var.llm_base_url },
    var.llm_api_key == null ? {} : { SANDBOXD_LLM_API_KEY = var.llm_api_key },
    { for k, v in var.sandbox_env : "SANDBOXD_SANDBOX_ENV_${k}" => v },
  )
}

module "api" {
  source = "../../modules/api"

  flake             = local.flake
  architecture      = var.architecture
  instance_type     = var.api_instance_type
  allowed_ssh_cidrs = var.allowed_ssh_cidrs
  admin_user        = var.admin_user
  ssh_public_keys   = var.ssh_public_keys
  tailscale_host    = var.tailnet == "" ? "" : "sandboxd-api.${var.tailnet}"

  hostname          = var.domain
  preview_domain    = var.preview_domain
  acme_email        = var.acme_email
  acme_dns_provider = "cloudflare"
  acme_env          = { CF_DNS_API_TOKEN = var.cloudflare_dns_api_token }
  env               = local.api_env
}

module "worker" {
  source = "../../modules/worker"

  flake             = local.flake
  architecture      = var.architecture
  instance_type     = var.worker_instance_type
  allowed_ssh_cidrs = var.allowed_ssh_cidrs
  admin_user        = var.admin_user
  ssh_public_keys   = var.ssh_public_keys
  tailscale_host    = var.tailnet == "" ? "" : "sandboxd-worker.${var.tailnet}"

  api_hostname = var.domain
}

resource "cloudflare_dns_record" "api" {
  zone_id = var.cloudflare_zone_id
  name    = var.domain
  type    = "A"
  content = module.api.ip
  proxied = false
  ttl     = 300
}

resource "cloudflare_dns_record" "preview" {
  zone_id = var.cloudflare_zone_id
  name    = "*.${var.preview_domain}"
  type    = "A"
  content = module.api.ip
  proxied = false
  ttl     = 300
}

output "api_url" {
  value = module.api.url
}

output "api_ip" {
  value = module.api.ip
}

output "worker_ip" {
  value = module.worker.ip
}
