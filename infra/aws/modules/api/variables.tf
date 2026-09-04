variable "flake" {
  description = "Flake exposing nixosConfigurations.<nixos_configuration>: a path (\"../..\") or a URL (\"github:org/sandboxd\")."
  type        = string
}

variable "nixos_configuration" {
  description = "nixosConfigurations attribute in the flake. null = aws-api (arm64) or aws-api-x86_64."
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

variable "tailscale_host" {
  description = "MagicDNS name of this host (traefik dashboard lives there). Empty disables tailscale."
  type        = string
  default     = ""
}

variable "extra_settings" {
  description = "Merged over the NixOS `settings` specialArg (sandboxd.host.*)."
  type        = any
  default     = {}
}

variable "env" {
  description = "KEY=value pairs for /etc/sandboxd/api.env: SANDBOXD_SERVICE_TOKEN, SANDBOXD_SECRET, SANDBOXD_LLM_*, SANDBOXD_SANDBOX_ENV_*."
  type        = map(string)
  sensitive   = true
}

variable "hostname" {
  description = "Public host of the control plane, e.g. sandboxd.example.com (a record in cloudflare_zone_id)."
  type        = string
}

variable "preview_domain" {
  description = "Preview wildcard, e.g. preview.sandboxd.example.com. Records are unproxied and traefik holds a Let's Encrypt wildcard, so any depth works."
  type        = string
}

variable "acme_email" {
  type = string
}

variable "cloudflare_zone_id" {
  type = string
}

variable "cloudflare_dns_api_token" {
  description = "Zone DNS:Edit token for the Let's Encrypt DNS-01 challenge (traefik)."
  type        = string
  sensitive   = true
}

variable "name" {
  description = "Instance name and key pair name."
  type        = string
  default     = "sandboxd-api"
}

variable "instance_type" {
  description = "t4g.nano (512 MB) is too small for a rebuild; micro is the floor."
  type        = string
  default     = "t4g.micro"
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
  type    = number
  default = 20
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
