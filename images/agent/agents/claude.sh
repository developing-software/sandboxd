#!/bin/bash
# claude: Anthropic's Claude Code. Talks to ANTHROPIC_BASE_URL, which _common.sh points at
# the gateway when one is configured; otherwise api.anthropic.com with ANTHROPIC_API_KEY.

agent_describe="Claude Code (@anthropic-ai/claude-code)"
agent_model_default="claude-sonnet-4-6"

agent_setup() {
  [ -n "${MODEL:-}" ] && export ANTHROPIC_MODEL="$MODEL"
  # Skip Claude Code's first-run gates: theme/onboarding, "trust this folder?", and the
  # "Bypass Permissions mode" confirmation. Login is skipped whenever ANTHROPIC_API_KEY
  # or ANTHROPIC_AUTH_TOKEN is set.
  mkdir -p "$HOME/.claude"
  cat > "$HOME/.claude.json" <<JSON
{
  "hasCompletedOnboarding": true,
  "theme": "dark",
  "bypassPermissionsModeAccepted": true,
  "projects": { "$PWD": { "hasTrustDialogAccepted": true } }
}
JSON
  cat > "$HOME/.claude/settings.json" <<JSON
{
  "permissions": { "defaultMode": "bypassPermissions" }
}
JSON
  export IS_SANDBOX=1 # allows bypass mode even as root
  export DISABLE_AUTOUPDATER=1
  export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
}

agent_run() {
  say "starting claude${MODEL:+ ($MODEL)}${BASE:+ via $BASE}"
  claude --dangerously-skip-permissions "${PROMPT:-}"
}
