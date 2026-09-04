output "ip" {
  description = "VM address on the bridge (the deploy target)."
  value       = local.target
}

output "url" {
  value = "https://${var.hostname}"
}

output "tunnel_id" {
  value = cloudflare_zero_trust_tunnel_cloudflared.this.id
}
