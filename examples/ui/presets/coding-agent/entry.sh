#!/bin/bash
# Entry of the coding-agent preset image. Runs inside the PTY.
# This is the image-side half of the preset; the daemon itself only guarantees
# TERM and SANDBOXD_SESSION_ID. Everything else is plain env (see preset.yaml):
#   REPO BRANCH BASE_BRANCH   clone target (REPO empty = skip cloning)
#   AGENT                     claude|codex|opencode|shell (default: claude when PROMPT is set, else shell)
#   PROMPT MODEL              handed to the agent (MODEL default claude-sonnet-4-6; CODEX_MODEL wins for codex)
#   SETUP                     shell command run in the clone before the agent (optional)
#   LLM_BASE_URL              OpenAI/Anthropic-compatible gateway root, e.g. a LiteLLM proxy (no /v1)
#   LLM_API_KEY               key for that gateway (secret)
#   GIT_TOKEN ANTHROPIC_API_KEY (secrets, optional)
# Operators set defaults for any of these with SANDBOXD_SANDBOX_ENV_<NAME> on the control plane.
# Ends in a shell no matter what, so the user can inspect failures or `git push`.
set -u
cd /workspace

# Defaults that used to live on the control plane. Only applied when unset, so
# a caller's value or an operator's SANDBOXD_SANDBOX_ENV_* always wins.
if [ -z "${AGENT:-}" ]; then
  if [ -n "${PROMPT:-}" ]; then AGENT=claude; else AGENT=shell; fi
fi
if [ -z "${MODEL:-}" ]; then
  # Codex only speaks the Responses API; through LiteLLM that path is OpenAI-models-only in practice.
  case "$AGENT" in codex) MODEL="${CODEX_MODEL:-claude-sonnet-4-6}" ;; *) MODEL=claude-sonnet-4-6 ;; esac
fi
export AGENT MODEL

if [ -n "${GIT_TOKEN:-}" ]; then
  export GIT_ASKPASS=/usr/local/bin/sandboxd-git-askpass
  export GIT_TERMINAL_PROMPT=0
fi
git config --global user.name  "${GIT_AUTHOR_NAME:-sandboxd}"
git config --global user.email "${GIT_AUTHOR_EMAIL:-sandboxd@localhost}"
git config --global init.defaultBranch main

# LLM gateway wiring + per-agent config writers.
. /usr/local/bin/sandboxd-agent-setup

run_agent() {
  case "$AGENT" in
    shell)
      echo "▶ agent=shell; dropping to a shell." ;;
    claude)
      setup_claude
      echo "▶ starting claude${MODEL:+ ($MODEL)}${BASE:+ via $BASE}"
      claude --dangerously-skip-permissions "${PROMPT:-}" ;;
    codex)
      setup_codex
      case "$MODEL" in claude*) echo "⚠ codex with a Claude model: codex uses the Responses API, and LiteLLM's Responses→Bedrock translation is known to fail (tool_choice conflict). Prefer an OpenAI model for codex (set CODEX_MODEL, e.g. SANDBOXD_SANDBOX_ENV_CODEX_MODEL on the control plane)." ;; esac
      echo "▶ starting codex${MODEL:+ ($MODEL)}${BASE:+ via $BASE/v1}"
      codex --dangerously-bypass-approvals-and-sandbox "${PROMPT:-}" ;;
    opencode)
      setup_opencode
      echo "▶ starting opencode${MODEL:+ ($MODEL)}${BASE:+ via $BASE/v1}"
      opencode --prompt "${PROMPT:-}" ;;
    *)
      echo "▶ unknown agent '$AGENT'; dropping to a shell." ;;
  esac
}

if [ -z "${REPO:-}" ]; then
  echo "▶ no REPO set; skipping clone."
  run_agent
elif git clone ${BASE_BRANCH:+--branch "$BASE_BRANCH"} "$REPO" . 2>&1; then
  git checkout -b "${BRANCH:-sandboxd/${SANDBOXD_SESSION_ID:-work}}" 2>&1
  echo "▶ on branch $(git branch --show-current)"
  if [ -n "${SETUP:-}" ]; then
    echo "▶ setup: $SETUP"
    bash -lc "$SETUP" || echo "⚠ setup exited with $?; continuing"
  fi
  run_agent
  echo
  echo "▶ agent exited. You are in a shell; \`git push -u origin $(git branch --show-current)\` when ready."
else
  echo "▶ clone failed; dropping to a shell so you can see why."
fi
exec bash -l
