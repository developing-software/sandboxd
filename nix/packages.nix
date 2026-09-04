# Both apps compile to one static binary each with `bun build --compile`. The source is
# the whole workspace minus tests: bun refuses a lockfile whose workspaces are missing.
{ lib, bun2nix }:
let
  root = ../.;
  src = lib.fileset.toSource {
    inherit root;
    fileset =
      lib.fileset.difference
        (lib.fileset.unions [
          (root + "/package.json")
          (root + "/bun.lock")
          (root + "/tsconfig.json")
          (root + "/apps/api")
          (root + "/apps/worker")
          (root + "/apps/ui/package.json")
          (root + "/packages/core")
        ])
        (
          lib.fileset.unions [
            (root + "/apps/api/test")
            (root + "/apps/worker/test")
            (root + "/packages/core/test")
          ]
        );
  };
  bunDeps = bun2nix.fetchBunDeps { bunNix = ./bun.nix; };

  mk =
    pname: module:
    bun2nix.mkDerivation {
      inherit
        pname
        src
        bunDeps
        module
        ;
      version = "0.1.0";
      # main.ts uses top-level await, which bytecode (CommonJS) cannot express.
      bunCompileToBytecode = false;
      # The root postinstall regenerates nix/bun.nix; pointless inside the sandbox.
      dontRunLifecycleScripts = true;
      meta.mainProgram = pname;
    };
in
{
  api = mk "sandboxd-api" "apps/api/src/main.ts";
  worker = mk "sandboxd-worker" "apps/worker/src/main.ts";
}
