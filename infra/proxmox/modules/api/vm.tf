# Identical in modules/worker: the VM, the nixos-anywhere install, the env upload, the
# rebuild. Kept inline so either module is one self-contained `source`.
locals {
  target       = coalesce(var.target_host, proxmox_virtual_environment_vm.vm.ipv4_addresses[1][0])
  install_user = coalesce(var.install_user, var.admin_user)
  attribute    = "${var.flake}#nixosConfigurations.${var.nixos_configuration}.config.system.build"
  env_file     = join("\n", concat([for k, v in local.env : "${k}=\"${replace(v, "\"", "\\\"")}\""], [""]))
}

resource "proxmox_virtual_environment_vm" "vm" {
  name        = var.name
  tags        = var.tags
  node_name   = var.node_name
  description = "sandboxd ${local.app}, NixOS via nixos-anywhere"
  boot_order  = ["scsi0", "ide2"]
  on_boot     = true

  agent {
    enabled = true
  }

  cpu {
    cores   = var.cores
    sockets = 1
    type    = "host"
  }

  memory {
    dedicated = var.memory_mb
  }

  operating_system {
    type = "l26"
  }

  disk {
    datastore_id = var.datastore_id
    file_format  = "raw"
    interface    = "scsi0"
    size         = var.disk_gb
  }

  cdrom {
    file_id   = var.iso_file_id
    interface = "ide2"
  }

  network_device {
    bridge = var.bridge
  }
}

# The host config is built locally; `settings` reaches NixOS as a specialArg through
# extendModules, so the flake stays owner-agnostic.
module "system" {
  source       = "github.com/nix-community/nixos-anywhere//terraform/nix-build"
  attribute    = "${local.attribute}.toplevel"
  special_args = local.special_args
}

module "disko" {
  source       = "github.com/nix-community/nixos-anywhere//terraform/nix-build"
  attribute    = "${local.attribute}.diskoScript"
  special_args = local.special_args
}

module "install" {
  source            = "github.com/nix-community/nixos-anywhere//terraform/install"
  nixos_system      = module.system.result.out
  nixos_partitioner = module.disko.result.out
  target_host       = local.target
  target_user       = local.install_user
  instance_id       = proxmox_virtual_environment_vm.vm.id
}

# Secrets never enter the store: they land in /etc/sandboxd/<app>.env after the install,
# then the units (which assert on the file) are restarted. Re-run when the content or
# the VM changes.
resource "terraform_data" "env" {
  depends_on       = [module.install]
  triggers_replace = [sha256(local.env_file), proxmox_virtual_environment_vm.vm.id]

  connection {
    type        = "ssh"
    host        = local.target
    user        = var.admin_user
    agent       = var.ssh_private_key_file == null
    private_key = var.ssh_private_key_file == null ? null : file(pathexpand(var.ssh_private_key_file))
  }

  provisioner "file" {
    content     = local.env_file
    destination = "/tmp/sandboxd-${local.app}.env"
  }

  provisioner "remote-exec" {
    inline = [
      "sudo install -d -m 0755 /etc/sandboxd",
      "sudo install -m 0600 -o root -g root /tmp/sandboxd-${local.app}.env /etc/sandboxd/${local.app}.env",
      "rm -f /tmp/sandboxd-${local.app}.env",
      "sudo systemctl restart ${join(" ", local.units)}",
    ]
  }
}

module "rebuild" {
  depends_on   = [terraform_data.env]
  source       = "github.com/nix-community/nixos-anywhere//terraform/nixos-rebuild"
  nixos_system = module.system.result.out
  target_host  = local.target
  target_user  = var.admin_user
}
