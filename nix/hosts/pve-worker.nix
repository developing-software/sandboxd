{ config, ... }:
{
  imports = [
    ./common.nix
    ./pve-vm.nix
  ];
  networking.hostName = "sandboxd-worker";

  services.sandboxd.worker = {
    enable = true;
    url = "wss://${config.sandboxd.host.domain}";
    environmentFile = "/etc/sandboxd/worker.env";
  };
}
