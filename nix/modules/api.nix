# services.sandboxd.api — the control plane. `settings` is /etc/sandboxd/api.yaml
# (internal/cp/config.go), rendered into the store, so it must never hold a secret: the
# module's defaults say `${SANDBOXD_SERVICE_TOKEN}` and the like, and environmentFile is
# where those live. Providers: workers only; sandboxes run on hosts that dial in.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.services.sandboxd.api;
  inherit (lib) mkOption types;
  yaml = pkgs.formats.yaml { };
  configFile = yaml.generate "api.yaml" cfg.settings;
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
      description = "What browsers are told to connect to (public_url).";
    };
    previewDomain = mkOption {
      type = types.str;
      example = "preview.sandboxd.example.com";
      description = "Preview proxy wildcard: <port>-<sid>.<previewDomain> (preview_domain).";
    };
    settings = mkOption {
      type = types.submodule { freeformType = yaml.type; };
      default = { };
      description = ''
        The whole of api.yaml. The options above fill the obvious keys; anything else —
        `sandbox_env`, `auth.secret` — goes here, as `''${VAR}` references when it is a
        secret, never as the value.
      '';
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
        KEY=value file with what the file reads through `''${VAR}`: SANDBOXD_SERVICE_TOKEN,
        and optionally SANDBOXD_SECRET, SANDBOXD_JOIN_TOKEN and whatever `sandbox_env`
        references. The unit refuses to start without it rather than fall back to "dev-token".
        To see the effective configuration on the host:
        `set -a; . /etc/sandboxd/api.env; set +a; sandboxd-api --config ${configFile} --check-config`.
      '';
    };
    openFirewall = mkOption {
      type = types.bool;
      default = false;
      description = "Expose the port directly. Off: only the reverse proxy on this host reaches it.";
    };
  };

  config = lib.mkIf cfg.enable {
    services.sandboxd.api.settings = {
      listen = lib.mkDefault ":${toString cfg.port}";
      public_url = lib.mkDefault cfg.publicUrl;
      preview_domain = lib.mkDefault cfg.previewDomain;
      db = lib.mkDefault "/var/lib/sandboxd-api/cp.db";
      auth = {
        service_token = lib.mkDefault "\${SANDBOXD_SERVICE_TOKEN}";
        secret = lib.mkDefault "\${SANDBOXD_SECRET:-}";
      };
      providers.workers.join_token = lib.mkDefault "\${SANDBOXD_JOIN_TOKEN:-}";
    };

    networking.firewall.allowedTCPPorts = lib.mkIf cfg.openFirewall [ cfg.port ];

    systemd.services.sandboxd-api = {
      description = "sandboxd control plane";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
      unitConfig.AssertPathExists = cfg.environmentFile;
      environment = cfg.extraEnv;
      serviceConfig = {
        ExecStart = "${lib.getExe cfg.package} --config ${configFile}";
        EnvironmentFile = cfg.environmentFile;
        DynamicUser = true;
        StateDirectory = "sandboxd-api";
        StateDirectoryMode = "0700";
        Restart = "always";
        RestartSec = 2;
        # A static Go binary never maps a page writable and executable; the TypeScript
        # daemon it replaced needed W^X toggling for Bun's JIT and could not have this.
        MemoryDenyWriteExecute = true;
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
