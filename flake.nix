{
  description = "sandboxd: control plane and per-host worker, built and deployed with Nix";

  # The org cache (developing-software/infra, stacks/nix-cache) carries the api and worker
  # builds. The rest of a host closure comes from cache.nixos.org; these two are the only
  # things nothing upstream has, and on arm64 they are built under emulation.
  nixConfig = {
    extra-substituters = [
      "https://nix-cache.developing.company"
      "https://nix-community.cachix.org"
    ];
    extra-trusted-public-keys = [
      "nix-cache.developing.company-1:LL1H3Pj8yNXnCgRLxUYrYv7WdTol8RJpcvVEqVv8atY="
      "nix-community.cachix.org-1:mB9FSh9qf2dCimDSUo8Zy7bkq5CX+/rkCWyvRCYg3Fs="
    ];
  };

  inputs = {
    systems.url = "github:nix-systems/default-linux";
    # Same branch as the official NixOS AMIs (nixos/26.05*) the AWS modules boot.
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";
    disko = {
      url = "github:nix-community/disko";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      systems,
      disko,
    }:
    let
      inherit (nixpkgs) lib;
      forAllSystems =
        f: lib.genAttrs (import systems) (system: f system nixpkgs.legacyPackages.${system});
      inputs = {
        inherit nixpkgs disko;
      };

      mkHost =
        system: host:
        lib.nixosSystem {
          inherit system;
          specialArgs = {
            inherit inputs;
          };
          modules = [
            self.nixosModules.default
            { nixpkgs.hostPlatform = system; }
            host
          ];
        };
    in
    {
      packages = forAllSystems (
        system: pkgs:
        let
          built = pkgs.callPackage ./nix/packages.nix { };
        in
        {
          inherit (built) api worker;
          default = built.api;
        }
      );

      overlays.default = final: _prev: {
        sandboxd-api = self.packages.${final.stdenv.hostPlatform.system}.api;
        sandboxd-worker = self.packages.${final.stdenv.hostPlatform.system}.worker;
      };

      nixosModules = {
        api =
          { pkgs, lib, ... }:
          {
            imports = [ ./nix/modules/api.nix ];
            services.sandboxd.api.package = lib.mkDefault self.packages.${pkgs.stdenv.hostPlatform.system}.api;
          };
        worker =
          { pkgs, lib, ... }:
          {
            imports = [ ./nix/modules/worker.nix ];
            services.sandboxd.worker.package =
              lib.mkDefault
                self.packages.${pkgs.stdenv.hostPlatform.system}.worker;
          };
        ingress = ./nix/modules/ingress.nix;
        host = ./nix/modules/host.nix;
        tunnel = ./nix/modules/tunnel.nix;
        default = {
          imports = with self.nixosModules; [
            api
            worker
            ingress
            tunnel
            host
          ];
        };
      };

      # One host per (infra, app), owner-agnostic: domain, admin user and keys arrive as
      # the `settings` specialArg (terraform passes it through nixos-anywhere's special_args,
      # applied with extendModules) or as `sandboxd.host.*` options in a consumer flake.
      # The aws-* pair follows the t4g (arm64) AMI; -x86_64 variants serve `architecture = "x86_64"`.
      nixosConfigurations = {
        pve-api = mkHost "x86_64-linux" ./nix/hosts/pve-api.nix;
        pve-worker = mkHost "x86_64-linux" ./nix/hosts/pve-worker.nix;
        aws-api = mkHost "aarch64-linux" ./nix/hosts/aws-api.nix;
        aws-worker = mkHost "aarch64-linux" ./nix/hosts/aws-worker.nix;
        aws-api-x86_64 = mkHost "x86_64-linux" ./nix/hosts/aws-api.nix;
        aws-worker-x86_64 = mkHost "x86_64-linux" ./nix/hosts/aws-worker.nix;
      };

      formatter = forAllSystems (_system: pkgs: pkgs.nixfmt);

      checks = forAllSystems (
        system: pkgs:
        let
          # Hosts require sandboxd.host.domain, so they are exercised with example settings
          # exactly as terraform injects real ones. drvPath instantiates without building.
          example = {
            domain = "sandboxd.example.com";
            previewDomain = "preview.sandboxd.example.com";
            acmeEmail = "ops@example.com";
            admin.sshKeys = [ "ssh-ed25519 AAAAexample" ];
          };
          hosts = lib.filterAttrs (
            _: h: h.pkgs.stdenv.hostPlatform.system == system
          ) self.nixosConfigurations;
          toplevel =
            h: (h.extendModules { specialArgs.settings = example; }).config.system.build.toplevel.drvPath;
        in
        {
          inherit (self.packages.${system}) api worker;
          hosts = pkgs.runCommand "sandboxd-hosts" {
            drvs = lib.mapAttrsToList (_: toplevel) hosts;
          } "touch $out";
        }
      );

      devShells = forAllSystems (
        system: pkgs: {
          default = pkgs.mkShell {
            packages = with pkgs; [
              # The one example client and its preset tooling.
              bun
              nodejs_24
              # The two daemons: compiler, LSP, linter, formatter.
              go
              gopls
              golangci-lint
              gofumpt
              opentofu
              nixos-anywhere
              awscli2
            ];
          };
        }
      );
    };
}
