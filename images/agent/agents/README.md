# agents/

One file per agent. This folder **is** the list of agents the image can run: `entry.sh`
discovers `*.sh` here at start-up, so adding a harness is adding a file — no Dockerfile
change beyond installing the binary, and no TypeScript anywhere knows an agent's name.

In the image the folder lands at `/opt/sandboxd/agents`. `SANDBOXD_AGENTS_DIR` points the
entry somewhere else, which is how you try an agent without rebuilding: mount a folder
over it, or bind-mount a single extra `.sh` into the stock one.

`sandboxd-agents` inside a sandbox prints what the running image has.

## The contract

A file named `<agent>.sh` is the agent `<agent>`. It is **sourced**, not executed, after
`_common.sh` and with the clone as the working directory. A leading underscore means
library, not agent (`_common.sh`), and is skipped by discovery.

| Name                  | Kind     | Required | What                                                            |
| --------------------- | -------- | -------- | --------------------------------------------------------------- |
| `agent_run`           | function | yes      | Runs the agent in the foreground on `$PROMPT`. Its exit is the agent's. |
| `agent_setup`         | function | no       | Writes config, exports env. Runs once, just before `agent_run`.  |
| `agent_model_default` | variable | no       | `MODEL` when the caller sent none. Empty = the vendor's own default. |
| `agent_describe`      | variable | no       | One line, shown by `sandboxd-agents`.                            |

What the entry has already done by then: cloned `REPO` on `BRANCH`, run `SETUP`, and
sourced `_common.sh` — so `OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL` and `OPENAI_API_KEY`
are set when a gateway is configured.

Read from the environment, never write to it outside `agent_setup`:

| Variable   | What                                                                       |
| ---------- | --------------------------------------------------------------------------- |
| `PROMPT`   | The task. May be empty — an empty prompt resolves to `shell` before you run. |
| `MODEL`    | Already defaulted from `agent_model_default`.                               |
| `BASE`     | Gateway root, no trailing `/v1`. Empty = talk to the vendor directly.       |
| `say`/`warn` | Print a `▶` line / a `⚠` line. Use them instead of bare `echo`.            |

Nothing else is guaranteed. The daemon promises only `TERM` and `SANDBOXD_SESSION_ID`
(`DESIGN.md` decision 5); everything else is env the preset mapped (see `../preset.yaml`).

## Adding one

1. `apt`/`npm` install the binary in `../Dockerfile`.
2. Write `<agent>.sh` here.
3. Add the name to `agent.values` in `../preset.yaml`, so the UI offers it and a typo is a
   400 rather than a shell. `bun test` fails if the two lists disagree.

The agent never has to handle its own failure: `entry.sh` always ends in a login shell, so
a crash leaves the user inside the clone with the output still on screen.
