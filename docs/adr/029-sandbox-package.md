# 29. `internal/sandbox`: the manager is shared code

Status: accepted, 2026-09-06.

## Decision

The PTY, ring, fanout and idle reaper move out of `internal/worker` into
`internal/sandbox`, and `internal/worker/driver` becomes `internal/sandbox/docker`. The
package imports nothing of ours but `wire`, and both daemons import it — the worker behind
its tunnel, the control plane in-process (decision 30). It is shared code, not a channel
between the daemons: they still meet on the wire, and `depguard` denies it both `cp` and
`worker`.

- `Dial` joins the `Driver` interface. Every driver has one, and a provider inside the
  control plane has no tunnel to declare a separate dialer on. `Manager.Dial(sid, port)`
  replaces the tunnel's `ContainerOf` plus dial.
- The manager takes `entry` and `max` at construction and exposes `Capacity()`. The
  worker's hello and heartbeat read capacity from there, not from its config, which is
  what lets the control plane reuse it unchanged.
- `sandbox/drivers` is the registry: `drivers.Config` is the `driver:` block on a worker and
  the driver half of `providers.<name>` on the control plane, and `Open` is the one switch
  from a name to a package. Neither cmd names Docker; a new driver is one field and two
  cases there.
- `sandbox.Tags(driver, extra)` is where `arch:`, `os:` and `driver:` are added, so a
  worker and an embedded provider report alike.
- The fake driver is `sandbox/sandboxtest`, one fake for every owner of a manager. The
  manager's own tests are external and reach the few internals they need through
  `export_test.go`.

## Rejected

- **Copying `manager.go` into `cp/local`.** The whole reason for the move.
- **Names `runtime` and `host`.** The first is the standard library's; `cp/hosts` exists.
