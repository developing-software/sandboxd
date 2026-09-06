variable "flake" {
  description = "Flake exposing nixosConfigurations.<nixos_configuration>: a path (\"../..\") or a URL (\"github:org/sandboxd\")."
  type        = string
}

variable "nixos_configuration" {
  description = "nixosConfigurations attribute in the flake."
  type        = string
  default     = "pve-worker"
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
  description = "MagicDNS name of this host. Empty disables tailscale."
  type        = string
  default     = ""
}

variable "extra_settings" {
  description = "Merged over the NixOS `settings` specialArg (sandboxd.host.*); `worker` inside it is merged into worker.yaml."
  type        = any
  default     = {}
}

variable "env" {
  description = "KEY=value pairs for /etc/sandboxd/worker.env, what worker.yaml reads through $${VAR}: SANDBOXD_JOIN_TOKEN. Registry credentials too. The worker's own identity is self-generated on the host."
  type        = map(string)
  sensitive   = true
  default     = {}
}

variable "api_hostname" {
  description = "Public host of the control plane this worker dials (wss://<api_hostname>)."
  type        = string
}

variable "name" {
  description = "VM name."
  type        = string
  default     = "sandboxd-worker"
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
  description = "Sandboxes are Docker containers on this VM; size for max_sessions."
  type        = number
  default     = 4
}

variable "memory_mb" {
  type    = number
  default = 8192
}

variable "disk_gb" {
  description = "Holds the preset images and every sandbox's writable layer."
  type        = number
  default     = 64
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
