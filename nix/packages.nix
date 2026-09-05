# Both daemons are one Go module at the repo root, and each cmd/ is one static binary.
# CGO is off, so `nix build --system aarch64-linux` cross-compiles without binfmt or an
# arm64 builder — which is the whole reason the port happened (PLAN.md).
{ lib, buildGoModule }:
let
  root = ../.;
  src = lib.fileset.toSource {
    inherit root;
    fileset = lib.fileset.unions [
      (root + "/go.mod")
      (root + "/go.sum")
      (root + "/cmd")
      (root + "/internal")
    ];
  };

  mk =
    pname: subPackage:
    buildGoModule {
      inherit pname src;
      version = "0.1.0";
      # One vendor tree for both binaries. Regenerate after a dependency change: set it to
      # lib.fakeHash, run `nix build .#api`, and copy the hash the error reports.
      vendorHash = "sha256-OQwRGbY/BNa4GNvZl746jSA2+pbEu8Iux9NqUdlO9U4=";
      subPackages = [ subPackage ];
      env.CGO_ENABLED = 0;
      ldflags = [
        "-s"
        "-w"
      ];
      # The live Docker check needs an Engine, which the sandbox has not got; the rest of
      # the suite runs in CI.
      doCheck = false;
      meta.mainProgram = pname;
    };
in
{
  api = mk "sandboxd-api" "cmd/sandboxd-api";
  worker = mk "sandboxd-worker" "cmd/sandboxd-worker";
}
