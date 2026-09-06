# One control plane behind a Cloudflare Tunnel and one worker, both NixOS VMs on Proxmox.
locals {
  flake = "../../../.."
  # The same join token on both ends: the worker enrols on first hello, no code to paste.
  join_env = var.join_token == null ? {} : { SANDBOXD_JOIN_TOKEN = var.join_token }
  # The env file holds the secrets. api.yaml is rendered by the NixOS module into the
  # store, so it must not hold one: `api_settings` says which value reads which variable.
  api_env = merge(
    { SANDBOXD_SERVICE_TOKEN = var.service_token },
    local.join_env,
    var.llm_base_url == null ? {} : { SANDBOXD_LLM_BASE_URL = var.llm_base_url },
    var.llm_api_key == null ? {} : { SANDBOXD_LLM_API_KEY = var.llm_api_key },
    { for k, v in var.sandbox_env : "SANDBOXD_SANDBOX_ENV_${k}" => v },
  )
  api_settings = {
    sandbox_env = merge(
      var.llm_base_url == null ? {} : { LLM_BASE_URL = "$${SANDBOXD_LLM_BASE_URL}" },
      var.llm_api_key == null ? {} : { LLM_API_KEY = "$${SANDBOXD_LLM_API_KEY}" },
      { for k, v in var.sandbox_env : k => "$${SANDBOXD_SANDBOX_ENV_${k}}" },
    )
  }
}

module "api" {
  source = "../../modules/api"

  flake                = local.flake
  node_name            = var.proxmox_node
  iso_file_id          = var.iso_file_id
  admin_user           = var.admin_user
  ssh_public_keys      = var.ssh_public_keys
  ssh_private_key_file = var.ssh_private_key_file
  tailscale_host       = var.tailnet == "" ? "" : "sandboxd-api.${var.tailnet}"

  hostname              = var.domain
  preview_domain        = var.preview_domain
  cloudflare_account_id = var.cloudflare_account_id
  cloudflare_zone_id    = var.cloudflare_zone_id
  extra_settings        = { api = local.api_settings }
  env                   = local.api_env
}

module "worker" {
  source = "../../modules/worker"

  flake                = local.flake
  node_name            = var.proxmox_node
  iso_file_id          = var.iso_file_id
  admin_user           = var.admin_user
  ssh_public_keys      = var.ssh_public_keys
  ssh_private_key_file = var.ssh_private_key_file
  tailscale_host       = var.tailnet == "" ? "" : "sandboxd-worker.${var.tailnet}"

  api_hostname = var.domain
  env          = local.join_env
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
