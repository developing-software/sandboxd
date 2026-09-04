output "ip" {
  description = "Elastic IP (the deploy target)."
  value       = aws_eip.this.public_ip
}

output "url" {
  value = "https://${var.hostname}"
}

output "instance_id" {
  value = aws_instance.this.id
}
