# Identical in modules/worker: the AMI, the instance, the env upload, the rebuild.
# Kept inline so either module is one self-contained `source`.
locals {
  nixos_configuration = coalesce(var.nixos_configuration, var.architecture == "arm64" ? local.app_config : "${local.app_config}-x86_64")
  attribute           = "${var.flake}#nixosConfigurations.${local.nixos_configuration}.config.system.build.toplevel"
  vpc_id              = coalesce(var.vpc_id, one(data.aws_vpc.default[*].id))
  env_file            = join("\n", concat([for k, v in local.env : "${k}=\"${replace(v, "\"", "\\\"")}\""], [""]))
}

# Official NixOS images, published by the NixOS foundation account.
data "aws_ami" "nixos" {
  owners      = ["427812963091"]
  most_recent = true

  filter {
    name   = "name"
    values = ["nixos/${var.nixos_release}*"]
  }
  filter {
    name   = "architecture"
    values = [var.architecture]
  }
}

data "aws_vpc" "default" {
  count   = var.vpc_id == null ? 1 : 0
  default = true
}

resource "aws_key_pair" "this" {
  key_name   = var.name
  public_key = var.ssh_public_keys[0]
}

resource "aws_instance" "this" {
  ami                    = data.aws_ami.nixos.id
  instance_type          = var.instance_type
  key_name               = aws_key_pair.this.key_name
  subnet_id              = var.subnet_id
  vpc_security_group_ids = [aws_security_group.this.id]
  tags                   = merge(var.tags, { Name = var.name })

  root_block_device {
    volume_size = var.root_volume_gb
    volume_type = "gp3"
  }
}

resource "aws_eip" "this" {
  instance = aws_instance.this.id
  domain   = "vpc"
  tags     = merge(var.tags, { Name = var.name })
}

# The host config is built locally (arm64 needs an aarch64 builder or binfmt); `settings`
# reaches NixOS as a specialArg through extendModules, so the flake stays owner-agnostic.
module "system" {
  source       = "github.com/nix-community/nixos-anywhere//terraform/nix-build"
  attribute    = local.attribute
  special_args = { settings = local.settings }
}

# The AMI seeds root's authorized_keys from the key pair, so uploads run as root. On the
# first apply the units do not exist yet; the rebuild below starts them.
resource "terraform_data" "env" {
  triggers_replace = [sha256(local.env_file), aws_instance.this.id]

  connection {
    type = "ssh"
    host = aws_eip.this.public_ip
    user = "root"
  }

  provisioner "file" {
    content     = local.env_file
    destination = "/tmp/sandboxd-${local.app}.env"
  }

  provisioner "remote-exec" {
    inline = [
      "install -d -m 0755 /etc/sandboxd",
      "install -m 0600 -o root -g root /tmp/sandboxd-${local.app}.env /etc/sandboxd/${local.app}.env",
      "rm -f /tmp/sandboxd-${local.app}.env",
      "if systemctl cat ${local.units[0]} >/dev/null 2>&1; then systemctl restart ${join(" ", local.units)}; fi",
    ]
  }
}

module "rebuild" {
  depends_on   = [terraform_data.env, terraform_data.traefik_env]
  source       = "github.com/nix-community/nixos-anywhere//terraform/nixos-rebuild"
  nixos_system = module.system.result.out
  target_host  = aws_eip.this.public_ip
  target_user  = "root"
}
