terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 5.0"
    }
  }

  # State in Cloudflare R2 (it holds the uploaded secrets). R2 auth is a static key
  # pair in its own profile so it never shadows the SSO profile the aws provider uses:
  #   ~/.aws/credentials  [r2]  aws_access_key_id / aws_secret_access_key
  backend "s3" {
    bucket  = "tfstate"
    key     = "sandboxd/aws-a3data-dev-agents.tfstate"
    region  = "auto"
    profile = "r2"

    endpoints = { s3 = "https://ffa7e183ef8bb75d1cfa0664ba324dee.r2.cloudflarestorage.com" }

    # R2 is not really S3: skip the AWS-isms, keep the lockfile.
    use_lockfile                = true
    skip_credentials_validation = true
    skip_metadata_api_check     = true
    skip_region_validation      = true
    skip_requesting_account_id  = true
    skip_s3_checksum            = true
    use_path_style              = true
  }
}

# `aws sso login --profile <aws_profile>` first. The zone-scoped DNS token traefik gets is
# all this env needs from Cloudflare, so the provider uses it too.
provider "aws" {
  region  = var.region
  profile = var.aws_profile
}

provider "cloudflare" {
  api_token = var.cloudflare_dns_api_token
}
