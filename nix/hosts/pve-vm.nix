# A Proxmox VM installed by nixos-anywhere: qemu guest, removable-EFI grub (Proxmox
# VMs have no EFI vars by default), DHCP, and the disko layout below.
{
  lib,
  inputs,
  modulesPath,
  ...
}:
{
  imports = [
    (modulesPath + "/installer/scan/not-detected.nix")
    (modulesPath + "/profiles/qemu-guest.nix")
    inputs.disko.nixosModules.disko
    ./disko.nix
  ];

  boot.loader.grub = {
    enable = true;
    device = "nodev";
    efiSupport = true;
    efiInstallAsRemovable = true;
  };
  boot.loader.efi.canTouchEfiVariables = false;

  services.qemuGuest.enable = true;
  networking.useDHCP = lib.mkDefault true;
}
