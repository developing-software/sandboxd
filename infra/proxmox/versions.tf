terraform {
  required_version = ">= 1.6"
  required_providers {
    proxmox = {
      source  = "bpg/proxmox"
      version = ">= 0.89.0"
    }
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 5.0"
    }
  }
  # Add a backend here (s3/minio, cloud, ...): the state holds the uploaded secrets.
}

# Auth via PROXMOX_VE_API_TOKEN (or PROXMOX_VE_USERNAME/PROXMOX_VE_PASSWORD) and CLOUDFLARE_API_TOKEN.
provider "proxmox" {
  endpoint = var.proxmox_endpoint
  insecure = var.proxmox_insecure

  ssh {
    agent = true
  }
}

provider "cloudflare" {}
