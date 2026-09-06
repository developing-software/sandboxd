variable "flake" {
  description = "Flake exposing nixosConfigurations.<nixos_configuration>: a path (\"../..\") or a URL (\"github:org/sandboxd\")."
  type        = string
}

variable "nixos_configuration" {
  description = "nixosConfigurations attribute in the flake."
  type        = string
  default     = "pve-api"
}

variable "admin_user" {
  description = "Admin user on the host: passwordless sudo, nix trusted-user, the ssh deploy target."
  type        = string
  default     = "admin"
}

variable "ssh_public_keys" {
  description = "authorized_keys of the admin user."
  type        = list(string)
}

variable "ssh_private_key_file" {
  description = "Private key for the env upload. null = the ssh agent, which fails with gcr-ssh-agent (GNOME keyring); pass a key file there."
  type        = string
  default     = null
}

variable "tailscale_host" {
  description = "MagicDNS name of this host (traefik dashboard lives there). Empty disables tailscale."
  type        = string
  default     = ""
}

variable "extra_settings" {
  description = "Merged over the NixOS `settings` specialArg (sandboxd.host.*); `api` inside it is merged into api.yaml."
  type        = any
  default     = {}
}

variable "env" {
  description = "KEY=value pairs for /etc/sandboxd/api.env, the secrets api.yaml reads through $${VAR}: SANDBOXD_SERVICE_TOKEN, and whatever `extra_settings.api` references (SANDBOXD_SECRET, SANDBOXD_JOIN_TOKEN, SANDBOXD_LLM_*, SANDBOXD_SANDBOX_ENV_*)."
  type        = map(string)
  sensitive   = true
}

variable "hostname" {
  description = "Public host of the control plane, e.g. sandboxd.example.com (a record in cloudflare_zone_id)."
  type        = string
}

variable "preview_domain" {
  description = "Preview wildcard. Behind the tunnel this must be the zone apex: Cloudflare's Universal SSL covers one label under the zone only."
  type        = string
}

variable "tunnel_port" {
  description = "Loopback port traefik listens on for cloudflared (services.sandboxd.ingress.tunnelPort)."
  type        = number
  default     = 8000
}

variable "cloudflare_account_id" {
  type = string
}

variable "cloudflare_zone_id" {
  type = string
}

variable "name" {
  description = "VM name; also the tunnel name."
  type        = string
  default     = "sandboxd-api"
}

variable "node_name" {
  type    = string
  default = "pve"
}

variable "datastore_id" {
  type    = string
  default = "local-zfs"
}

variable "iso_file_id" {
  description = "NixOS installer ISO on the node, e.g. local:iso/nixos-minimal-26.05-x86_64-linux.iso."
  type        = string
}

variable "bridge" {
  type    = string
  default = "vmbr0"
}

variable "cores" {
  type    = number
  default = 2
}

variable "memory_mb" {
  type    = number
  default = 2048
}

variable "disk_gb" {
  type    = number
  default = 32
}

variable "tags" {
  type    = list(string)
  default = ["terraform", "nixos", "sandboxd"]
}

variable "target_host" {
  description = "SSH address for install and rebuild. null = the VM's first IPv4 as reported by the qemu agent."
  type        = string
  default     = null
}

variable "install_user" {
  description = "SSH user on the booted installer ISO (root or passwordless sudo). null = admin_user, which fits a custom ISO; the stock ISO wants root."
  type        = string
  default     = null
}
