# services.sandboxd.worker — the per-host daemon. Enables Docker and runs the worker in
# the docker group; its self-generated secret persists in the state directory, so the
# control plane keeps recognising the host across reboots and upgrades.
{
  config,
  lib,
  ...
}:
let
  cfg = config.services.sandboxd.worker;
  inherit (lib) mkOption types;
in
{
  options.services.sandboxd.worker = {
    enable = lib.mkEnableOption "the sandboxd worker daemon";
    package = mkOption {
      type = types.package;
      description = "The sandboxd-worker package (set by the flake's nixosModules.worker).";
    };
    url = mkOption {
      type = types.str;
      example = "wss://sandboxd.example.com";
      description = "The control plane to dial (SANDBOXD_URL).";
    };
    name = mkOption {
      type = types.nullOr types.str;
      default = null;
      description = "Host name shown in the control plane (SANDBOXD_WORKER_NAME); null = hostname.";
    };
    maxSessions = mkOption {
      type = types.ints.positive;
      default = 4;
      description = "Concurrent sandboxes on this host (SANDBOXD_WORKER_MAX_SESSIONS).";
    };
    entry = mkOption {
      type = types.listOf types.str;
      default = [ "/usr/local/bin/sandboxd-entry" ];
      description = "Command exec'd in the PTY inside every sandbox (SANDBOXD_WORKER_ENTRY).";
    };
    tags = mkOption {
      type = types.listOf types.str;
      default = [ ];
      example = [
        "virt:vm"
        "region:eu"
      ];
      description = ''
        Host tags a sandbox may require (SANDBOXD_WORKER_TAGS). Opaque strings compared by
        set containment; `key:value` is a convention, not a schema. `arch:`, `os:` and
        `driver:` are reported without being configured.
      '';
    };
    extraEnv = mkOption {
      type = types.attrsOf types.str;
      default = { };
      description = "Extra non-secret environment for the unit.";
    };
    environmentFile = mkOption {
      type = types.nullOr types.str;
      default = null;
      example = "/etc/sandboxd/worker.env";
      description = "Optional KEY=value file: SANDBOXD_JOIN_TOKEN to enrol without the printed code, registry credentials. The worker's own identity is self-generated.";
    };
  };

  config = lib.mkIf cfg.enable {
    virtualisation.docker.enable = true;

    systemd.services.sandboxd-worker = {
      description = "sandboxd worker";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      requires = [ "docker.service" ];
      after = [
        "network-online.target"
        "docker.service"
      ];
      environment = {
        SANDBOXD_URL = cfg.url;
        SANDBOXD_WORKER_MAX_SESSIONS = toString cfg.maxSessions;
        SANDBOXD_WORKER_IDENTITY = "/var/lib/sandboxd-worker/host.json";
        SANDBOXD_WORKER_ENTRY = builtins.toJSON cfg.entry;
        DOCKER_SOCK = "/var/run/docker.sock";
      }
      // lib.optionalAttrs (cfg.tags != [ ]) { SANDBOXD_WORKER_TAGS = lib.concatStringsSep "," cfg.tags; }
      // lib.optionalAttrs (cfg.name != null) { SANDBOXD_WORKER_NAME = cfg.name; }
      // cfg.extraEnv;
      serviceConfig = {
        ExecStart = lib.getExe cfg.package;
        EnvironmentFile = lib.mkIf (cfg.environmentFile != null) cfg.environmentFile;
        DynamicUser = true;
        SupplementaryGroups = [ "docker" ];
        StateDirectory = "sandboxd-worker";
        StateDirectoryMode = "0700";
        Restart = "always";
        RestartSec = 5;
        NoNewPrivileges = true;
        PrivateTmp = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        ProtectKernelTunables = true;
        ProtectControlGroups = true;
        # A static Go binary never maps a page writable and executable; the TypeScript
        # daemon it replaced needed W^X toggling for Bun's JIT and could not have this.
        MemoryDenyWriteExecute = true;
        LockPersonality = true;
      };
    };
  };
}
