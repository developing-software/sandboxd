# One control plane with a public IP and one worker on the official NixOS AMI, DNS in
# Cloudflare. The modules know nothing about DNS: a Route53 env swaps the two records
# below for aws_route53_record and sets acme_dns_provider = "route53".
locals {
  flake = "../../../.."
  # The same join token on both ends: the worker enrols on first hello, no code to paste.
  join_env = var.join_token == null ? {} : { SANDBOXD_JOIN_TOKEN = var.join_token }
  api_env = merge(
    { SANDBOXD_SERVICE_TOKEN = var.service_token },
    local.join_env,
    var.llm_base_url == null ? {} : { SANDBOXD_LLM_BASE_URL = var.llm_base_url },
    var.llm_api_key == null ? {} : { SANDBOXD_LLM_API_KEY = var.llm_api_key },
    { for k, v in var.sandbox_env : "SANDBOXD_SANDBOX_ENV_${k}" => v },
  )
}

module "api" {
  source = "../../modules/api"

  flake                = local.flake
  architecture         = var.architecture
  instance_type        = var.api_instance_type
  allowed_ssh_cidrs    = var.allowed_ssh_cidrs
  admin_user           = var.admin_user
  ssh_public_keys      = var.ssh_public_keys
  ssh_private_key_file = var.ssh_private_key_file
  tailscale_host       = var.tailnet == "" ? "" : "sandboxd-api.${var.tailnet}"

  hostname          = var.domain
  preview_domain    = var.preview_domain
  acme_email        = var.acme_email
  acme_dns_provider = "cloudflare"
  acme_env          = { CF_DNS_API_TOKEN = var.cloudflare_dns_api_token }
  env               = local.api_env
}

# One block for the whole fleet. A worker reports `arch:` itself (decision 8), so a host
# needs no configuration to be selectable: every one here enrols with the same join token,
# generates its own secret, and the scheduler places an `arch:amd64` sandbox on the t3 and
# an `arch:arm64` one on the t4g. Adding a host is a `workers` entry: everything a host is
# named after is derived from `name`, so there is no second place to edit.
module "worker" {
  source = "../../modules/worker"
  # Keyed by name rather than position, so removing a worker does not renumber — and so
  # does not replace — the ones after it.
  for_each = { for w in var.workers : w.name => w }

  flake                = local.flake
  architecture         = each.value.architecture
  instance_type        = each.value.instance_type
  name                 = "sandboxd-worker-${each.key}"
  allowed_ssh_cidrs    = var.allowed_ssh_cidrs
  admin_user           = var.admin_user
  ssh_public_keys      = var.ssh_public_keys
  ssh_private_key_file = var.ssh_private_key_file
  tailscale_host       = var.tailnet == "" ? "" : "sandboxd-worker-${each.key}.${var.tailnet}"

  api_hostname = var.domain
  env          = local.join_env
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

# One entry per worker rather than an output per host: the fleet is a list that grows, and
# the architecture is what you look one up by (it is the `arch:` tag a sandbox asks for).
output "workers" {
  value = [
    for w in var.workers : {
      name = w.name
      arch = w.architecture
      ip   = module.worker[w.name].ip
    }
  ]
}
