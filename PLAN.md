# Plan: sandboxes from more than one kind of host

Temporary. This is the delta between the code and the shape below; a phase is deleted from
here when it lands, and the file goes when the last one does. `VISION.md` is the permanent
half. Decisions and their rejected alternatives are ADRs under `docs/adr/`.

Landed so far: the config file (ADR 28), `internal/sandbox` (ADR 29), the provider seam
and `providers.docker` (ADR 30). Two words, used precisely from here on:

- A **driver** is *what runs a sandbox*: `docker` today; `kubernetes`, `qemu` next. One
  package under `internal/sandbox/`, shared by both daemons, with one config struct that
  decodes from the same block in either file.
- A **provider** is *where the manager that owns the PTY runs*: behind the tunnel
  (`workers`), or inside the control plane (`providers.<driver>`, which is `cp/local` over
  that driver; no new provider package per driver).

## The Kubernetes driver (phase 4)

The one runtime where the API server already proxies both halves of the contract:
`pods/exec` with a TTY and `pods/portforward`. One Pod per sandbox, `restartPolicy: Never`,
one container with `command: ["sleep", "infinity"]` (the same idle trick as Docker), then
exec over the `v5.channel.k8s.io` websocket subprotocol for the PTY and `portforward.k8s.io`
for `Dial`. `ListManaged` is a label selector (`sandboxd.io/sid`, `sandboxd.io/owner`). Env
travels in the exec request, so no secret lands in an object the API server persists.
Credentials from the in-cluster service account files or a kubeconfig (`clusters`, `users`,
`contexts` with token or client cert; anything else is an error naming the field). Runs in
a worker (`driver.kubernetes`) or in the control plane (`providers.kubernetes` is
`cp/local` over this driver), from the same `kubernetes.Config` in both. Not `client-go`:
thirty modules for one JSON POST and two websocket subprotocols, against a closed
dependency list.

```yaml
  kubernetes:                                   # under providers: or driver:
    name: k8s                                   # providers: only
    tags: [arch:amd64]                          # providers: only
    kubeconfig: ""                              # empty = in-cluster
    namespace: sandboxes
    max_sandboxes: 20
    pod:
      runtime_class: gvisor
      node_selector: {pool: sandboxes}
      service_account: ""
      image_pull_policy: Always
      create_timeout_s: 300
      resources: {limits: {cpu: "2", memory: 4Gi}}
```

A Pod that does not reach `Running` within `pod.create_timeout_s` ends `failed`; the exec
stream closing ends `exited`; `Destroy` deletes with `gracePeriodSeconds: 0`. Until it
lands, a `kubernetes` block is an unknown key and refuses to boot.

**Touches:** `sandbox/kubernetes` (`client.go`, `pod.go`, `exec.go`, `portforward.go`,
`kubeconfig.go`); `worker/config.go` (`driver.kubernetes`); `cp/config.go`,
`cmd/sandboxd-api`; RBAC in the deploy notes (`pods` create/get/list/delete/watch,
`pods/exec`, `pods/portforward`, one namespace).

**Done when:** exec and portforward framing pass against an `httptest.Server` speaking the
subprotocols; kubeconfig parsing handles token, client cert, and rejects `exec:`; the
manager suite runs against this driver behind `sandboxtest`'s shape; a live
`SANDBOXD_K8S_TEST=1` run against `kind` creates, attaches, dials, destroys, sweeps clean.

## Later, in the order they earn their keep

1. **`docker.host` and `docker.tls`.** The moby client already speaks `tcp://` with client
   certs, so pointing the Docker driver at a remote engine on a private network is two
   keys in its block, on either daemon. One block per driver stops being enough at two
   engines; a list under `providers.docker.engines` is the likely shape, with Swarm node
   discovery as an optional source of it. Decide then.
2. **`sandbox/qemu`.** Worker-only. A VM per sandbox, a PTY on the serial console or a
   vsock agent, `Dial` over a tap. How an image becomes a disk is the spec's main question.
3. **`sandbox/podman`.** Likely the Docker driver over a different socket; verify first.

## Not doing

See ADR 30: no Swarm provider, no ECS provider, no `provider` field in the API. And no env
overriding the file per scalar (ADR 28).

Every phase leaves the gate green:
`golangci-lint fmt && go vet ./... && golangci-lint run && go test ./...`.
