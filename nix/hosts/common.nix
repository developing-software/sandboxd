# What every sandboxd host shares: the admin user the terraform modules ssh in as
# (passwordless sudo + nix trusted-user, which the nixos-anywhere `nixos-rebuild` step
# needs to push unsigned closures), optional tailscale, a few tools.
{ config, pkgs, ... }:
let
  host = config.sandboxd.host;
in
{
  imports = [ ../modules/host.nix ];

  users.users.${host.admin.user} = {
    isNormalUser = true;
    extraGroups = [ "wheel" ];
    openssh.authorizedKeys.keys = host.admin.sshKeys;
  };
  security.sudo.wheelNeedsPassword = false;

  services.openssh = {
    enable = true;
    settings.PasswordAuthentication = false;
  };

  nix.settings = {
    experimental-features = "nix-command flakes";
    auto-optimise-store = true;
    trusted-users = [
      "root"
      host.admin.user
    ];
  };

  services.tailscale.enable = host.tailscale.enable;
  services.resolved = {
    enable = true;
    settings.Resolve.DNSSEC = "false";
  };

  environment.systemPackages = with pkgs; [
    helix
    git
    htop
  ];

  networking.firewall.enable = true;
  system.stateVersion = "26.05";
}
