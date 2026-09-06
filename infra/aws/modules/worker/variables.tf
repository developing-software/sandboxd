variable "flake" {
  description = "Flake exposing nixosConfigurations.<nixos_configuration>: a path (\"../..\") or a URL (\"github:org/sandboxd\")."
  type        = string
}

variable "nixos_configuration" {
  description = "nixosConfigurations attribute in the flake. null = aws-worker (arm64) or aws-worker-x86_64."
  type        = string
  default     = null
}

variable "admin_user" {
  description = "Admin user on the host: passwordless sudo, nix trusted-user. Deploys run as root (the AMI seeds root's key)."
  type        = string
  default     = "admin"
}

variable "ssh_public_keys" {
  description = "authorized_keys of the admin user; the first one becomes the instance key pair."
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
  description = "Instance name and key pair name."
  type        = string
  default     = "sandboxd-worker"
}

variable "instance_type" {
  description = "Sandboxes are Docker containers on this instance; size it for max_sessions."
  type        = string
  default     = "t4g.medium"
}

variable "architecture" {
  description = "AMI architecture; must match instance_type (t4g = arm64, t3 = x86_64)."
  type        = string
  default     = "arm64"
  validation {
    condition     = contains(["arm64", "x86_64"], var.architecture)
    error_message = "architecture must be arm64 or x86_64."
  }
}

variable "nixos_release" {
  description = "Official NixOS AMI name prefix to pick (nixos/<release>*)."
  type        = string
  default     = "26.05"
}

variable "root_volume_gb" {
  description = "Holds the preset images and every sandbox's writable layer."
  type        = number
  default     = 64
}

variable "vpc_id" {
  description = "null = the default VPC."
  type        = string
  default     = null
}

variable "subnet_id" {
  type    = string
  default = null
}

variable "allowed_ssh_cidrs" {
  type    = list(string)
  default = ["0.0.0.0/0"]
}

variable "tags" {
  type    = map(string)
  default = { project = "sandboxd" }
}
