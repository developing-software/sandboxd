variable "proxmox_endpoint" {
  type = string
}

variable "proxmox_insecure" {
  description = "Accept the node's self-signed certificate."
  type        = bool
  default     = true
}

variable "proxmox_node" {
  type    = string
  default = "pve"
}

variable "iso_file_id" {
  description = "NixOS installer ISO on the node, e.g. local:iso/nixos-minimal-26.05-x86_64-linux.iso."
  type        = string
}

variable "domain" {
  description = "Public host of the control plane, in the Cloudflare zone."
  type        = string
}

variable "preview_domain" {
  description = "Zone apex (see modules/api on Universal SSL)."
  type        = string
}

variable "tailnet" {
  description = "MagicDNS suffix, e.g. example.ts.net. Empty disables tailscale."
  type        = string
  default     = ""
}

variable "admin_user" {
  type    = string
  default = "admin"
}

variable "ssh_public_keys" {
  type = list(string)
}

variable "ssh_private_key_file" {
  description = "Key matching ssh_public_keys[0], used by the env uploads instead of the ssh agent."
  type        = string
  default     = null
}

variable "cloudflare_account_id" {
  type = string
}

variable "cloudflare_zone_id" {
  type = string
}

variable "service_token" {
  description = "SANDBOXD_SERVICE_TOKEN: what parent apps present to the API."
  type        = string
  sensitive   = true
}

variable "join_token" {
  description = "SANDBOXD_JOIN_TOKEN on the API and the worker: the worker enrols without the printed code. null = approve by code in the UI."
  type        = string
  sensitive   = true
  default     = null
}

variable "llm_base_url" {
  type    = string
  default = null
}

variable "llm_api_key" {
  type      = string
  sensitive = true
  default   = null
}

variable "sandbox_env" {
  description = "Operator env injected into every sandbox (SANDBOXD_SANDBOX_ENV_<NAME>)."
  type        = map(string)
  default     = {}
}
