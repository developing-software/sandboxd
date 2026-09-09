locals {
  app   = "worker"
  units = ["sandboxd-worker.service"]
  settings = merge({
    domain        = var.api_hostname
    previewDomain = var.api_hostname
    admin         = { user = var.admin_user, sshKeys = var.ssh_public_keys }
    tailscale     = { enable = var.tailscale_host != "", host = var.tailscale_host }
  }, var.extra_settings)
  # nixos-anywhere's nix-build.sh splices special_args into a Nix ''…'' string, where
  # `${` is interpolation; `''${` is its escape and lands as a literal `${` for conf.
  special_args = { settings = jsondecode(replace(jsonencode(local.settings), "$${", "''$${")) }
  env          = var.env
}
