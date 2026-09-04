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
  description = "arm64 (t4g) or x86_64 (t3). An arm64 closure needs an aarch64 builder or binfmt on the machine running tofu."
  type        = string
  default     = "arm64"
}

variable "api_instance_type" {
  type    = string
  default = "t4g.micro"
}

variable "worker_instance_type" {
  type    = string
  default = "t4g.medium"
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
