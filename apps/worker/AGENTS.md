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
  memory right after. Nothing is set at container create.
- **One session, one container.** No sidecars, no per-session network. A session that
  needs more runs it inside the sandbox.
- **Orphans are found by label.** Every container carries the `sandboxd.*` labels from
  `docker.ts`; anything created without them is invisible to cleanup.
- The worker is a per-session choice of _what_ to run only through `Msg.Spec`. It knows
  nothing about repos, agents or models.

## Commands

From this directory: `bun test`, `bun run typecheck`.
