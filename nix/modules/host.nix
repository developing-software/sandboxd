# sandboxd.host — the per-deployment facts a host profile needs and this repo must not
# know: who administers the box, where it is reachable. A consumer sets these options in
# its own flake; the terraform modules pass them as the `settings` specialArg through
# nixos-anywhere's special_args, which extendModules merges in at build time.
{ config, lib, ... }@args:
let
  inherit (lib) mkOption types;
  # A specialArg, read through @args so a flake that never sets it still evaluates.
  settings = args.settings or { };
in
{
  options.sandboxd.host = {
    admin = {
      user = mkOption {
        type = types.str;
        default = "admin";
        description = "Admin user: passwordless sudo, nix trusted-user, the deploy target.";
      };
      sshKeys = mkOption {
        type = types.listOf types.str;
        default = [ ];
        description = "authorized_keys for the admin user.";
      };
    };
    domain = mkOption {
      type = types.str;
      example = "sandboxd.example.com";
      description = "Public host of the control plane; workers dial wss://<domain>.";
    };
    previewDomain = mkOption {
      type = types.str;
      example = "preview.sandboxd.example.com";
      description = ''
        Preview wildcard, <port>-<sid>.<previewDomain>. Behind a Cloudflare Tunnel this
        must be the zone apex: Universal SSL covers one label under the zone only.
      '';
    };
    acmeEmail = mkOption {
      type = types.str;
      default = "";
      description = "ACME account email (hosts that terminate TLS themselves).";
    };
    acmeDnsProvider = mkOption {
      type = types.str;
      default = "cloudflare";
      example = "route53";
      description = "Lego DNS-01 provider for the wildcard cert; its credentials come from /etc/sandboxd/traefik.env.";
    };
    tailscale = {
      enable = mkOption {
        type = types.bool;
        default = false;
        description = "Join a tailnet (auth key via `tailscale up` after the first boot).";
      };
      host = mkOption {
        type = types.str;
        default = "";
        example = "sandboxd-api.tailnet.ts.net";
        description = "MagicDNS name; when set, the traefik dashboard is served there.";
      };
    };
  };

  config.sandboxd.host = lib.mapAttrsRecursive (_: v: lib.mkDefault v) settings;
}
