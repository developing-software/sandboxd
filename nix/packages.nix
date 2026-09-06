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
      # api/ is a Go package: the control plane embeds the two documents it was generated
      # from and serves those bytes, rather than a second rendering of them.
      (root + "/api")
      (root + "/cmd")
      (root + "/internal")
    ];
  };

  mk =
    pname: subPackage:
    buildGoModule {
      inherit pname src;
      version = "0.1.0";
      # One module cache for both binaries. Regenerate after a dependency change: set it to
      # lib.fakeHash, run `nix build .#api`, and copy the hash the error reports.
      #
      # The proxy rather than a vendor tree, because ogen is a `tool` in go.mod: `go mod
      # vendor` writes tool dependencies into vendor/ without marking them explicit in
      # modules.txt, and the build then refuses its own vendor directory. Nothing here
      # runs the generator — internal/gen is checked in — so
      # this is the cost of pinning the generator's version beside the runtime's.
      proxyVendor = true;
      vendorHash = "sha256-+Ovr6f7IxJyBzl7ONMzoPMZQeJYvT8F83GlC6jgdHxk=";
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
