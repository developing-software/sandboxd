{ config, ... }:
let
  host = config.sandboxd.host;
in
{
  imports = [
    ./common.nix
    ./pve-vm.nix
  ];
  networking.hostName = "sandboxd-api";

  services.sandboxd.api = {
    enable = true;
    publicUrl = "https://${host.domain}";
    previewDomain = host.previewDomain;
  };
  services.sandboxd.ingress = {
    enable = true;
    inherit (host) previewDomain;
    host = host.domain;
    tls = "none";
    tailscale = {
      enable = host.tailscale.enable && host.tailscale.host != "";
      host = host.tailscale.host;
    };
  };
  services.sandboxd.tunnel.enable = true;
}
