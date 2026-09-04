# services.sandboxd.tunnel — a remotely-managed Cloudflare Tunnel. Ingress rules live in
# Cloudflare (terraform writes them); this host only needs the connector token. The
# stock services.cloudflared module keys tunnels by id at eval time, and the id only
# exists after `tofu apply`, so this is a plain unit instead.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.services.sandboxd.tunnel;
  inherit (lib) mkOption types;
in
{
  options.services.sandboxd.tunnel = {
    enable = lib.mkEnableOption "the cloudflared connector for the sandboxd ingress";
    package = mkOption {
      type = types.package;
      default = pkgs.cloudflared;
      defaultText = "pkgs.cloudflared";
      description = "cloudflared package.";
    };
    environmentFile = mkOption {
      type = types.str;
      default = "/etc/sandboxd/tunnel.env";
      description = "KEY=value file with TUNNEL_TOKEN.";
    };
  };

  config = lib.mkIf cfg.enable {
    systemd.services.sandboxd-tunnel = {
      description = "sandboxd cloudflared tunnel";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
      unitConfig.AssertPathExists = cfg.environmentFile;
      serviceConfig = {
        ExecStart = "${lib.getExe cfg.package} tunnel --no-autoupdate run";
        EnvironmentFile = cfg.environmentFile;
        DynamicUser = true;
        Restart = "always";
        RestartSec = 5;
        NoNewPrivileges = true;
        PrivateTmp = true;
        ProtectSystem = "strict";
        ProtectHome = true;
      };
    };
  };
}
