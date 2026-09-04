# services.sandboxd.ingress — traefik in front of the control plane, the way the vault
# host does it. Two routers share one upstream: the API host and the preview wildcard
# (<port>-<sid>.<previewDomain>; the API matches on the Host header, so it must pass
# through untouched). tls = "letsencrypt" terminates on this host with a Cloudflare
# DNS-01 wildcard; tls = "none" listens on a loopback entrypoint for cloudflared.
{
  config,
  lib,
  ...
}:
let
  cfg = config.services.sandboxd.ingress;
  inherit (lib) mkOption types;
  acme = cfg.tls == "letsencrypt";
  ts = cfg.tailscale.enable;
  certDomains = [
    {
      main = cfg.host;
      sans = [ "*.${cfg.previewDomain}" ];
    }
  ];
  routerTls = lib.optionalAttrs acme {
    tls = {
      certResolver = "letsencrypt";
      domains = certDomains;
    };
  };
in
{
  options.services.sandboxd.ingress = {
    enable = lib.mkEnableOption "traefik in front of the sandboxd control plane";
    host = mkOption {
      type = types.str;
      example = "sandboxd.example.com";
      description = "Public host of the control plane.";
    };
    previewDomain = mkOption {
      type = types.str;
      description = "Wildcard the preview proxy answers on; same value as services.sandboxd.api.previewDomain.";
    };
    upstream = mkOption {
      type = types.str;
      default = "http://127.0.0.1:${toString config.services.sandboxd.api.port}";
      defaultText = "http://127.0.0.1:<services.sandboxd.api.port>";
      description = "Where traefik forwards both routers.";
    };
    tls = mkOption {
      type = types.enum [
        "letsencrypt"
        "none"
      ];
      default = "letsencrypt";
      description = ''
        letsencrypt: :80/:443 with a Cloudflare DNS-01 wildcard (CF_DNS_API_TOKEN in
        acmeEnvironmentFile). none: TLS terminates elsewhere (Cloudflare Tunnel); traefik
        only listens on 127.0.0.1:tunnelPort.
      '';
    };
    acmeEmail = mkOption {
      type = types.str;
      default = "";
      description = "ACME account email (tls = letsencrypt).";
    };
    acmeDnsProvider = mkOption {
      type = types.str;
      default = "cloudflare";
      example = "route53";
      description = "Lego DNS-01 provider for the wildcard cert (tls = letsencrypt).";
    };
    acmeEnvironmentFile = mkOption {
      type = types.str;
      default = "/etc/sandboxd/traefik.env";
      description = "KEY=value file with that provider's credentials, e.g. CF_DNS_API_TOKEN (tls = letsencrypt).";
    };
    tunnelPort = mkOption {
      type = types.port;
      default = 8000;
      description = "Loopback entrypoint cloudflared targets (tls = none).";
    };
    tailscale = {
      enable = mkOption {
        type = types.bool;
        default = false;
        description = "Serve the traefik dashboard at https://<tailscale.host>/traefik with a tailscale cert.";
      };
      host = mkOption {
        type = types.str;
        default = "";
        example = "sandboxd-api.tailnet.ts.net";
        description = "This machine's MagicDNS name.";
      };
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion = !acme || cfg.acmeEmail != "";
        message = "services.sandboxd.ingress.acmeEmail is required with tls = letsencrypt";
      }
      {
        assertion = !ts || cfg.tailscale.host != "";
        message = "services.sandboxd.ingress.tailscale.host is required with tailscale.enable";
      }
    ];

    networking.firewall.allowedTCPPorts = lib.mkIf acme [
      80
      443
    ];
    networking.firewall.interfaces.tailscale0.allowedTCPPorts = lib.mkIf ts [ 443 ];
    services.tailscale.permitCertUid = lib.mkIf ts "traefik";

    services.traefik = {
      enable = true;
      environmentFiles = lib.mkIf acme [ cfg.acmeEnvironmentFile ];

      staticConfigOptions = {
        entryPoints =
          lib.optionalAttrs acme {
            web = {
              address = ":80";
              asDefault = true;
              http.redirections.entrypoint = {
                to = "websecure";
                scheme = "https";
              };
            };
          }
          // lib.optionalAttrs (acme || ts) {
            websecure = {
              address = ":443";
              asDefault = acme;
              http.tls = lib.optionalAttrs acme { certResolver = "letsencrypt"; };
            };
          }
          // lib.optionalAttrs (!acme) {
            tunnel = {
              address = "127.0.0.1:${toString cfg.tunnelPort}";
              asDefault = true;
              # cloudflared sets X-Forwarded-Proto; the API needs it for secure cookies.
              forwardedHeaders.trustedIPs = [ "127.0.0.1/32" ];
            };
          };

        log = {
          level = "INFO";
          format = "json";
        };

        certificatesResolvers =
          lib.optionalAttrs acme {
            letsencrypt.acme = {
              email = cfg.acmeEmail;
              storage = "${config.services.traefik.dataDir}/acme.json";
              dnsChallenge = {
                provider = cfg.acmeDnsProvider;
                resolvers = [
                  "1.1.1.1:53"
                  "1.0.0.1:53"
                ];
                # A poll before the TXT propagates gets a NODATA cached for the zone's
                # negative TTL (1800s on Cloudflare), and the wildcard check then times out.
                propagation.delayBeforeChecks = "30s";
              };
            };
          }
          // lib.optionalAttrs ts { vpnresolver.tailscale = { }; };

        api = lib.mkIf ts {
          dashboard = true;
          basepath = "/traefik";
        };
      };

      dynamicConfigOptions.http = {
        routers = {
          sandboxd-api = {
            rule = "Host(`${cfg.host}`)";
            service = "sandboxd";
          }
          // routerTls;
          sandboxd-preview = {
            rule = "HostRegexp(`^[0-9]+-s_[a-z2-7]+\\.${lib.escapeRegex cfg.previewDomain}$`)";
            service = "sandboxd";
          }
          // routerTls;
        }
        // lib.optionalAttrs ts {
          traefik-dashboard = {
            rule = "Host(`${cfg.tailscale.host}`) && PathPrefix(`/traefik`)";
            entryPoints = [ "websecure" ];
            service = "api@internal";
            tls.certResolver = "vpnresolver";
          };
        };
        services.sandboxd.loadBalancer.servers = [ { url = cfg.upstream; } ];
      };
    };
  };
}
