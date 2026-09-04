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
  # Add a backend here (s3, cloud, ...): the state holds the uploaded secrets.
}

# Auth via the usual AWS_* variables or profile, and CLOUDFLARE_API_TOKEN.
provider "aws" {
  region = var.region
}

provider "cloudflare" {}
