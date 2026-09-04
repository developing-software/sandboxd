# One control plane behind a Cloudflare Tunnel and one worker, both NixOS VMs on Proxmox.
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

  flake           = local.flake
  node_name       = var.proxmox_node
  iso_file_id     = var.iso_file_id
  admin_user      = var.admin_user
  ssh_public_keys = var.ssh_public_keys
  tailscale_host  = var.tailnet == "" ? "" : "sandboxd-api.${var.tailnet}"

  hostname              = var.domain
  preview_domain        = var.preview_domain
  cloudflare_account_id = var.cloudflare_account_id
  cloudflare_zone_id    = var.cloudflare_zone_id
  env                   = local.api_env
}

module "worker" {
  source = "../../modules/worker"

  flake           = local.flake
  node_name       = var.proxmox_node
  iso_file_id     = var.iso_file_id
  admin_user      = var.admin_user
  ssh_public_keys = var.ssh_public_keys
  tailscale_host  = var.tailnet == "" ? "" : "sandboxd-worker.${var.tailnet}"

  api_hostname = var.domain
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
