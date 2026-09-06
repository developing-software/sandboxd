# 28. One YAML file per daemon

Status: accepted, 2026-09-06. Lands with `internal/conf`.

## Decision

Each daemon reads one YAML file — `--config`, then `SANDBOXD_CONFIG`, then
`/etc/sandboxd/{api,worker}.yaml`. Traefik's shape: `providers:` keyed by type on the
control plane, presence enables; `driver:` on the worker, exactly one block. The `docker`
block is one struct, `docker.Config`, decoded from the same keys in either file.

- Strict: an unknown key refuses to boot, with the file, line and column.
- One source: with a file, the environment reaches the configuration only by `${VAR}`,
  `${VAR:-default}`, `${VAR:?message}` and `$$` in string values. Unset with no default is a
  boot error, never an empty string. Keys are not expanded; a bare `$HOME` passes through.
  Substitution runs on the parsed node tree, so a value that becomes a number still
  decodes as one and a fault still names a line.
- Every secret key has a `_file` twin; both set is an error.
- Every fault is reported in one call, joined.
- `--check-config` loads, validates, prints the source and the effective configuration
  with secrets redacted, and exits 0 or 1. With a file it also lists the `SANDBOXD_*`
  variables it is ignoring.
- No file, old behaviour: today's `SANDBOXD_*` variables apply unchanged. That keeps
  deployed units and `scripts/dev` booting; it is compatibility, not design.
  `SANDBOXD_WORKER_CONFIG` becomes `SANDBOXD_WORKER_IDENTITY`, the old name warned about
  for one release.
- `max_sandboxes` and `entry` move into the driver block: how many sandboxes a runtime can
  hold and what it execs are facts about the runtime, not about the process around it.

`github.com/go-faster/yaml` is promoted from indirect to direct: it was already in the
graph through ogen, and its errors carry positions.

## Rejected

- **Env overriding the file per scalar.** Two ways to set one value; `${VAR}` already lets
  the file say which values come from outside.
- **The decoder's own `KnownFields`.** It cannot run on a node tree, and substitution
  must. The unknown-key walk in `conf` is the one rule of the decoder's we reimplement,
  and it reports a column where the decoder would not.
- **A `provider` field in the API.** Tags select; `provider:workers` and `provider:local`
  are tags like any other.
