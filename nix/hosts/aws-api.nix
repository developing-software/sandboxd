{ config, ... }:
let
  host = config.sandboxd.host;
in
{
  imports = [
    ./common.nix
    ./aws.nix
  ];
  networking.hostName = "sandboxd-api";

  services.sandboxd.api = {
    enable = true;
    publicUrl = "https://${host.domain}";
    previewDomain = host.previewDomain;
  };
  services.sandboxd.ingress = {
    enable = true;
    inherit (host) previewDomain acmeEmail acmeDnsProvider;
    host = host.domain;
    tls = "letsencrypt";
    tailscale = {
      enable = host.tailscale.enable && host.tailscale.host != "";
      host = host.tailscale.host;
    };
  };
}
