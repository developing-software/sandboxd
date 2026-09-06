#!/bin/bash
# Entry of the sandboxd-agent image. Runs inside the PTY, and owns the semantics of this
# image: clone a repo on a fresh branch, run SETUP, hand the clone to one agent, end in a
# shell. The daemon guarantees only TERM and SANDBOXD_SESSION_ID; everything below is
# plain env, mapped from the request by whatever preset points at this image.
#
#   REPO BRANCH BASE_BRANCH   clone target (REPO empty = skip cloning)
#   AGENT                     a file in the agents dir (default: claude when PROMPT is set, else shell)
#   PROMPT                    the task, handed to the agent
#   MODEL                     default: the agent file's agent_model_default
#   SETUP                     shell command run in the clone before the agent (optional)
#   LLM_BASE_URL LLM_API_KEY  one gateway tuple, mapped onto each harness by agents/_common.sh
#   GIT_TOKEN GIT_USERNAME    for private repos / push
#   SANDBOXD_AGENTS_DIR       where the agents live (default /opt/sandboxd/agents)
#
# Operators set defaults for any of these with SANDBOXD_SANDBOX_ENV_<NAME> on the control
# plane. Ends in a shell no matter what, so the user can inspect a failure or `git push`.
set -u
cd /workspace || exit 1

AGENTS_DIR="${SANDBOXD_AGENTS_DIR:-/opt/sandboxd/agents}"
. "$AGENTS_DIR/_common.sh"

# Only applied when unset, so a caller's value or an operator's SANDBOXD_SANDBOX_ENV_* wins.
if [ -z "${AGENT:-}" ]; then
  if [ -n "${PROMPT:-}" ]; then AGENT=claude; else AGENT=shell; fi
fi
export AGENT

if [ -n "${GIT_TOKEN:-}" ]; then
  export GIT_ASKPASS=/usr/local/bin/sandboxd-git-askpass
  export GIT_TERMINAL_PROMPT=0
fi
git config --global user.name "${GIT_AUTHOR_NAME:-sandboxd}"
git config --global user.email "${GIT_AUTHOR_EMAIL:-sandboxd@localhost}"
git config --global init.defaultBranch main
git config --global --add safe.directory /workspace

# Load the agent. An unknown name is the user's typo, not a crash: say what exists and
# fall through to a shell, which is what the file named `shell` does anyway.
load_agent() {
  local file="$AGENTS_DIR/$AGENT.sh"
  if [ "${AGENT#_}" != "$AGENT" ] || [ ! -f "$file" ]; then
    warn "unknown agent '$AGENT'. This image has: $(sandboxd-agents --names | tr '\n' ' ')"
    AGENT=shell
    file="$AGENTS_DIR/shell.sh"
  fi
  agent_setup() { :; }
  agent_model_default=""
  # shellcheck source=/dev/null
  . "$file"
  [ -z "${MODEL:-}" ] && MODEL="$agent_model_default"
  export MODEL
}

run_agent() {
  load_agent
  agent_setup
  agent_run
}

if [ -z "${REPO:-}" ]; then
  say "no REPO set; skipping clone."
  run_agent
elif git clone ${BASE_BRANCH:+--branch "$BASE_BRANCH"} "$REPO" . 2>&1; then
  git checkout -b "${BRANCH:-sandboxd/${SANDBOXD_SESSION_ID:-work}}" 2>&1
  say "on branch $(git branch --show-current)"
  if [ -n "${SETUP:-}" ]; then
    say "setup: $SETUP"
    bash -lc "$SETUP" || warn "setup exited with $?; continuing"
  fi
  run_agent
  echo
  say "agent exited. You are in a shell; \`git push -u origin $(git branch --show-current)\` when ready."
else
  warn "clone failed; dropping to a shell so you can see why."
fi
exec bash -l
