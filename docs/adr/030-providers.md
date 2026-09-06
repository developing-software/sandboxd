# 30. Providers: sandboxes from more than one kind of host

Status: accepted, 2026-09-06.

Two words, used precisely. A **driver** is *what runs a sandbox*: `docker` today. A
**provider** is *where the manager that owns the PTY runs*: on a remote machine behind the
tunnel (`workers`), or inside the control plane process (`docker`, keyed by its driver).

## Decision

- `cp/fleet` implements the interfaces the scheduler, attach bridge, preview proxy and
  host admin already declared, by asking each provider whether a host id is its own. There
  is no registry: a provider answers `Online(id)`, and the fleet takes the first that says
  yes. A deployment with one provider may hand it to the scheduler directly.
- `cp/local` wraps one `sandbox.Manager` over any `sandbox.Driver` and presents it as one
  host: an approved row upserted at every boot, fingerprint
  `sha256("sandboxd/provider/<driver>")`, its id kept across restarts, tags from its block
  plus `provider:local`. It reports like a worker would — online with nothing running, a
  heartbeat on the same interval, started and ended as the manager emits them — so
  reconciliation covers a restart with no new path. `revoke` on it holds until the next
  boot re-approves it.
- Capacity is counted optimistically at `CreateSandbox` and resynced from the manager on
  every event and heartbeat, exactly as the hub does for a worker.
- Every host behind the tunnel carries `provider:workers`, added by enrollment: which
  provider a host belongs to is the control plane's fact, not the worker's.
- `cp.Providers.Embedded()` lists the in-process providers as name, tags and a
  `drivers.Config`, so `cmd/sandboxd-api` loops without naming a driver.
- `cp.PTY` is declared beside `cp.Capacity`, for the same reason: the fleet routes it
  between a provider and the bridge, and neither should have to name the other.
- `providers:` absent or empty means `workers: {}`; present means exactly what is present,
  so a file naming only `docker` runs no tunnel and `GET /tunnel` is a 404.

**The trade, written down:** a sandbox on a CP-hosted provider does not survive a CP
restart. Its PTY was an exec held by this process; at boot the provider reports nothing
running, the row reconciles to `lost`, the orphan sweep removes the container. The worker
path keeps that property; choosing a provider gives it up for one fewer daemon.

## Rejected

- **Providers registering host ids into the fleet.** Two calls in the hub and a map the
  fleet must keep in step with it; asking costs one map lookup per provider.
- **A Swarm provider.** Swarm has no `service exec`; a PTY on a task means reaching the
  owning node's engine, which Swarm does not proxy.
- **An ECS provider.** Fargate has no socket; ECS Exec is SSM's session protocol.
- **A `provider` field in the API**, or any API change at all.
