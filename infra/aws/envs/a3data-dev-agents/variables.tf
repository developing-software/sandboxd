variable "aws_profile" {
  description = "Named profile from ~/.aws/config. null = the default credential chain."
  type        = string
  default     = null
}

variable "region" {
  type    = string
  default = "us-east-2"
}

variable "architecture" {
  description = "The control plane's: arm64 (t4g) or x86_64 (t3). Workers carry their own, in `workers`. An arm64 closure needs an aarch64 builder or binfmt on the machine running tofu."
  type        = string
  default     = "arm64"
}

variable "api_instance_type" {
  type    = string
  default = "t4g.micro"
}

# One entry per worker host. `name` becomes `sandboxd-worker-<name>` and is free, so two of
# the same architecture are two entries. `architecture` is the AMI's word for it, while the
# host reports Go's — an x86_64 worker answers to `arch:amd64` in a sandbox's tags.
variable "workers" {
  type = list(object({
    name          = string
    architecture  = string
    instance_type = string
  }))
  default = [
    { name = "arm64", architecture = "arm64", instance_type = "t4g.medium" },
    { name = "amd64", architecture = "x86_64", instance_type = "t3.medium" },
  ]
}

variable "allowed_ssh_cidrs" {
  type    = list(string)
  default = ["0.0.0.0/0"]
}

variable "domain" {
  description = "Public host of the control plane, in the Cloudflare zone."
  type        = string
}

variable "preview_domain" {
  description = "Preview wildcard, e.g. preview.sandboxd.example.com."
  type        = string
}

variable "acme_email" {
  type = string
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

variable "cloudflare_zone_id" {
  type = string
}

variable "cloudflare_dns_api_token" {
  description = "Zone DNS:Edit token traefik uses for the Let's Encrypt DNS-01 challenge."
  type        = string
  sensitive   = true
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
