#!/bin/bash
# Runs inside the sandbox PTY. Env from the daemon:
#   REPO BRANCH BASE_BRANCH PROMPT AGENT(claude|codex|opencode|shell) MODEL
#   LLM_BASE_URL   OpenAI/Anthropic-compatible gateway root, e.g. a LiteLLM proxy (no /v1)
#   LLM_API_KEY    key for that gateway (secret)
#   GIT_TOKEN ANTHROPIC_API_KEY (secrets, optional)
# Ends in a shell no matter what, so the user can inspect failures or `git push`.
set -u
cd /workspace

if [ -n "${GIT_TOKEN:-}" ]; then
  export GIT_ASKPASS=/usr/local/bin/devagents-git-askpass
  export GIT_TERMINAL_PROMPT=0
fi
git config --global user.name  "${GIT_AUTHOR_NAME:-devagents}"
git config --global user.email "${GIT_AUTHOR_EMAIL:-devagents@localhost}"
git config --global init.defaultBranch main

# LLM gateway wiring + per-agent config writers.
. /usr/local/bin/devagents-agent-setup

run_agent() {
  case "${AGENT:-claude}" in
    shell)
      echo "▶ agent=shell; dropping to a shell in the cloned repo." ;;
    claude)
      setup_claude
      echo "▶ starting claude${MODEL:+ ($MODEL)}${BASE:+ via $BASE}"
      claude --dangerously-skip-permissions "$PROMPT" ;;
    codex)
      setup_codex
      echo "▶ starting codex${MODEL:+ ($MODEL)}${BASE:+ via $BASE/v1}"
      codex --dangerously-bypass-approvals-and-sandbox "$PROMPT" ;;
    opencode)
      setup_opencode
      echo "▶ starting opencode${MODEL:+ ($MODEL)}${BASE:+ via $BASE/v1}"
      opencode --prompt "$PROMPT" ;;
    *)
      echo "▶ unknown agent '$AGENT'; dropping to a shell." ;;
  esac
}

echo "▶ cloning $REPO"
if git clone ${BASE_BRANCH:+--branch "$BASE_BRANCH"} "$REPO" . 2>&1; then
  git checkout -b "$BRANCH" 2>&1
  echo "▶ on branch $BRANCH"
  run_agent
  echo
  echo "▶ agent exited. You are in a shell; \`git push -u origin $BRANCH\` when ready."
else
  echo "▶ clone failed; dropping to a shell so you can see why."
fi
exec bash -l
