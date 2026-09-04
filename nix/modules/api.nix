# services.sandboxd.api — the control plane. Every option is one SANDBOXD_* variable from
# apps/api/src/config.ts; secrets (SANDBOXD_SERVICE_TOKEN, SANDBOXD_SECRET, SANDBOXD_LLM_*,
# SANDBOXD_SANDBOX_ENV_*) come from environmentFile and never touch the store.
{
  config,
  lib,
  ...
}:
let
  cfg = config.services.sandboxd.api;
  inherit (lib) mkOption types;
in
{
  options.services.sandboxd.api = {
    enable = lib.mkEnableOption "the sandboxd control plane";
    package = mkOption {
      type = types.package;
      description = "The sandboxd-api package (set by the flake's nixosModules.api).";
    };
    port = mkOption {
      type = types.port;
      default = 8080;
      description = "Listen port; put traefik (services.sandboxd.ingress) in front of it.";
    };
    publicUrl = mkOption {
      type = types.str;
      example = "https://sandboxd.example.com";
      description = "What browsers are told to connect to (SANDBOXD_PUBLIC_URL).";
    };
    previewDomain = mkOption {
      type = types.str;
      example = "preview.sandboxd.example.com";
      description = "Preview proxy wildcard: <port>-<sid>.<previewDomain> (SANDBOXD_PREVIEW_DOMAIN).";
    };
    maxServices = mkOption {
      type = types.ints.positive;
      default = 8;
      description = "Sidecar services per session (SANDBOXD_MAX_SERVICES).";
    };
    extraEnv = mkOption {
      type = types.attrsOf types.str;
      default = { };
      description = "Extra non-secret environment for the unit.";
    };
    environmentFile = mkOption {
      type = types.str;
      default = "/etc/sandboxd/api.env";
      description = ''
        KEY=value file with SANDBOXD_SERVICE_TOKEN and optionally SANDBOXD_SECRET,
        SANDBOXD_JOIN_TOKEN, SANDBOXD_LLM_BASE_URL, SANDBOXD_LLM_API_KEY,
        SANDBOXD_SANDBOX_ENV_<NAME>.
        The unit refuses to start without it rather than fall back to "dev-token".
      '';
    };
    openFirewall = mkOption {
      type = types.bool;
      default = false;
      description = "Expose the port directly. Off: only the reverse proxy on this host reaches it.";
    };
  };

  config = lib.mkIf cfg.enable {
    networking.firewall.allowedTCPPorts = lib.mkIf cfg.openFirewall [ cfg.port ];

    systemd.services.sandboxd-api = {
      description = "sandboxd control plane";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
      unitConfig.AssertPathExists = cfg.environmentFile;
      environment = {
        SANDBOXD_PORT = toString cfg.port;
        SANDBOXD_PUBLIC_URL = cfg.publicUrl;
        SANDBOXD_PREVIEW_DOMAIN = cfg.previewDomain;
        SANDBOXD_MAX_SERVICES = toString cfg.maxServices;
        SANDBOXD_DB = "/var/lib/sandboxd-api/cp.db";
      }
      // cfg.extraEnv;
      serviceConfig = {
        ExecStart = lib.getExe cfg.package;
        EnvironmentFile = cfg.environmentFile;
        DynamicUser = true;
        StateDirectory = "sandboxd-api";
        StateDirectoryMode = "0700";
        Restart = "always";
        RestartSec = 2;
        # No MemoryDenyWriteExecute: Bun's JIT needs W^X toggling.
        NoNewPrivileges = true;
        PrivateTmp = true;
        PrivateDevices = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        ProtectKernelTunables = true;
        ProtectControlGroups = true;
        RestrictAddressFamilies = [
          "AF_INET"
          "AF_INET6"
          "AF_UNIX"
        ];
        LockPersonality = true;
      };
    };
  };
}
