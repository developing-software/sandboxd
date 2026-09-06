# images

The official sandbox images. One folder each, one image each, published to GHCR by
`.github/workflows/images.yml`.

These are the other half of the product. The API is generic — a sandbox is an image, a
command and env (`DESIGN.md` decision 20) — so everything a user would call a feature
("clone my repo and run Claude on it", "open this in VS Code") lives in an image's entry
script, not in the daemons. The control plane never knows which of these it is running.

| Image                                            | What                                                            | Preset that names it        |
| ------------------------------------------------ | ---------------------------------------------------------------- | --------------------------- |
| `ghcr.io/developing-software/sandboxd-agent`     | A repo on a fresh branch, handed to a coding agent               | `examples/ui/presets/agent` |
| `ghcr.io/developing-software/sandboxd-vscode`    | code-server on a repo, behind the preview proxy                  | `…/presets/vscode`          |
| `ghcr.io/developing-software/sandboxd-jupyter`   | JupyterLab or classic Notebook on a repo, behind the same proxy  | `…/presets/jupyter`         |

Presets are data and live with the reference client, in `examples/ui/presets/`. A preset
names an image and maps request fields onto env; it never builds one, and nothing here
imports anything from there. The two meet at a tag and a set of variable names — which is
the same arrangement any external image gets, and the reason a user's own image is a first
class one.

## Tags

| Tag              | From                                       |
| ---------------- | ------------------------------------------ |
| `latest`         | the default branch (`dev` today)           |
| `main`, `dev`    | the branch of that name                    |
| `v1.2.3`, `v1.2` | a `v*` git tag                             |
| `sha-abc1234`    | every published build                      |

The shipped presets name `:latest`, so a worker with no local build pulls without anyone
preparing it. That means a merge to the default branch moves what every worker gets on its
next miss — deliberate, and the reason `sha-` exists: point a `preset.yaml` at one to pin.

## Making a new one public (once, by hand)

**A package pushed by CI is private, and a public repository does not make it public** — a
linked package inherits the repository's access permissions but not its visibility. There
is no REST endpoint for this and no workflow setting; it is the web UI, once per package,
and it does not go back:

> [Organization → Packages](https://github.com/orgs/developing-software/packages) → the
> package → **Package settings** → Danger Zone → **Change visibility** → **Public**

Set **Org Settings → Packages → Package Creation** to allow Public so the next image out
of the matrix does not need this again.

Until a package is public, a worker pulling it gets `401 Unauthorized` and the sandbox ends
`failed` — indistinguishable from a tag that does not exist. Check with an anonymous pull:

```sh
curl -so /dev/null -w '%{http_code}\n' \
  https://ghcr.io/v2/developing-software/sandboxd-agent/manifests/latest    # 200 = public
```

## Building one locally

There is no build script: the context is the folder, so `docker build` is the whole thing.

```sh
docker build -t ghcr.io/developing-software/sandboxd-agent:latest images/agent
```

Tag it exactly as the preset names it. The worker only pulls on a 404, so a local build
under the published tag shadows the registry — which is how you try a change to an entry
script without pushing one. Every worker host needs the image; on a fleet, push to a
registry rather than building on each.

`images/agent` takes build args pinning the three agent CLIs
(`CLAUDE_CODE_VERSION`, `CODEX_VERSION`, `OPENCODE_VERSION`, `NODE_MAJOR`).

## What an image owes the daemon

The contract is small and the same for every image, ours or yours (`DESIGN.md`, "Contract
with any image"):

- Stay up on its own. The daemon starts the container, then execs the sandbox's command
  into a PTY of its own; `CMD ["sleep", "infinity"]` is why every image here ends that way.
- Read its configuration from env. Only `TERM` and `SANDBOXD_SESSION_ID` are promised by
  the daemon — everything else is whatever the caller (or a preset) sent.
- Install its entry at `/usr/local/bin/sandboxd-entry`. A preset over one of these images
  therefore has no `cmd:`; a preset over a stock image must say what runs in the PTY.

## Adding an image

1. `images/<name>/` with a `Dockerfile` and an `entry.sh` that ends in a shell.
2. Add `<name>` to the matrix in `.github/workflows/images.yml`.
3. A preset that names it, in `examples/ui/presets/<name>/preset.yaml` — or none, if the
   image is meant to be requested directly by tag.

For the agent image specifically, adding an *agent* is smaller than adding an image: it is
one file in `agent/agents/`. See [`agent/agents/README.md`](agent/agents/README.md).
