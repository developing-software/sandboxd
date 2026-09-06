# services.sandboxd.worker — the per-host daemon. Enables Docker and runs the worker in
# the docker group; its self-generated secret persists in the state directory, so the
# control plane keeps recognising the host across reboots and upgrades. `settings` is
# /etc/sandboxd/worker.yaml (internal/worker/config.go), rendered into the store, so the
# join token is a `${SANDBOXD_JOIN_TOKEN}` reference and lives in environmentFile.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.services.sandboxd.worker;
  inherit (lib) mkOption types;
  yaml = pkgs.formats.yaml { };
  configFile = yaml.generate "worker.yaml" cfg.settings;
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
      description = "The control plane to dial (url).";
    };
    name = mkOption {
      type = types.nullOr types.str;
      default = null;
      description = "Host name shown in the control plane (name); null = hostname.";
    };
    maxSessions = mkOption {
      type = types.ints.positive;
      default = 4;
      description = "Concurrent sandboxes on this host (driver.docker.max_sandboxes).";
    };
    entry = mkOption {
      type = types.listOf types.str;
      default = [ "/usr/local/bin/sandboxd-entry" ];
      description = "Command exec'd in the PTY inside every sandbox (driver.docker.entry).";
    };
    tags = mkOption {
      type = types.listOf types.str;
      default = [ ];
      example = [
        "virt:vm"
        "region:eu"
      ];
      description = ''
        Host tags a sandbox may require (tags). Opaque strings compared by set
        containment; `key:value` is a convention, not a schema. `arch:`, `os:`, `driver:`
        and `provider:workers` are reported without being configured.
      '';
    };
    settings = mkOption {
      type = types.submodule { freeformType = yaml.type; };
      default = { };
      description = "The whole of worker.yaml. The options above fill the obvious keys.";
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
    services.sandboxd.worker.settings = {
      url = lib.mkDefault cfg.url;
      name = lib.mkIf (cfg.name != null) (lib.mkDefault cfg.name);
      tags = lib.mkDefault cfg.tags;
      join_token = lib.mkDefault "\${SANDBOXD_JOIN_TOKEN:-}";
      identity = lib.mkDefault "/var/lib/sandboxd-worker/host.json";
      driver.docker = {
        sock = lib.mkDefault "/var/run/docker.sock";
        entry = lib.mkDefault (builtins.toJSON cfg.entry);
        max_sandboxes = lib.mkDefault cfg.maxSessions;
      };
    };

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
      environment = cfg.extraEnv;
      serviceConfig = {
        ExecStart = "${lib.getExe cfg.package} --config ${configFile}";
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
