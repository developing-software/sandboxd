# worker

The per-host daemon. Dials out to the control plane once (`tunnel.ts`), owns every PTY
and ring buffer (`pty.ts`, `sessions.ts`), and talks to Docker over the raw Engine API
(`docker.ts`) behind the `SandboxDriver` interface (`driver.ts`).

## Rules

- **Zero runtime dependencies.** This ships onto other people's machines. `@sandboxd/core`
  is the only import outside Bun and Node built-ins.
- **The driver is the seam.** Nothing above `driver.ts` may know it is Docker. Podman or
  Firecracker is a second implementation of the interface, not a branch.
- **Secrets are exec-time only.** `secret_env` goes into `docker exec` and is dropped from
  memory right after. Service `secret_env` is weaker by necessity (set at create); do not
  widen that.
- **Sidecars start in order and stop in reverse.** The sequential awaits in `sessions.ts`
  are the point; do not parallelise them.
- **Orphans are found by label.** Every container and network carries the `sandboxd.*`
  labels from `docker.ts`; anything created without them is invisible to cleanup.
- The worker is a per-session choice of _what_ to run only through `SessionSpec`. It
  knows nothing about repos, agents or models.

## Commands

From this directory: `bun test`, `bun run typecheck`.
