#!/bin/bash
# Sourced by cp-entry. Maps one gateway tuple (LLM_BASE_URL, LLM_API_KEY, MODEL)
# onto each harness's own configuration. Safe to source outside a session.

BASE="${LLM_BASE_URL:-}"; BASE="${BASE%/}"; BASE="${BASE%/v1}"
if [ -n "$BASE" ]; then
  export OPENAI_BASE_URL="$BASE/v1"
  export ANTHROPIC_BASE_URL="$BASE"
fi
if [ -n "${LLM_API_KEY:-}" ]; then
  export OPENAI_API_KEY="$LLM_API_KEY"
  # Claude Code sends ANTHROPIC_AUTH_TOKEN as a Bearer token, which is what LiteLLM expects.
  [ -z "${ANTHROPIC_API_KEY:-}" ] && export ANTHROPIC_AUTH_TOKEN="$LLM_API_KEY"
fi
[ -n "${MODEL:-}" ] && export ANTHROPIC_MODEL="$MODEL"

setup_codex() {
  mkdir -p "$HOME/.codex"
  {
    echo 'approval_policy = "never"'
    echo 'sandbox_mode = "danger-full-access"'
    [ -n "${MODEL:-}" ] && echo "model = \"$MODEL\""
    if [ -n "$BASE" ]; then
      echo 'model_provider = "gateway"'
      echo
      echo '[model_providers.gateway]'
      echo 'name = "LLM gateway"'
      echo "base_url = \"$BASE/v1\""
      echo 'env_key = "OPENAI_API_KEY"'
      echo 'wire_api = "responses"'   # codex >= 0.15x only speaks the Responses API; LiteLLM serves /v1/responses
    fi
  } > "$HOME/.codex/config.toml"
}

setup_opencode() {
  mkdir -p "$HOME/.config/opencode"
  local m="${MODEL:-gpt-4o}"
  [ -z "$BASE" ] && return 0
  cat > "$HOME/.config/opencode/opencode.json" <<JSON
{
  "\$schema": "https://opencode.ai/config.json",
  "provider": {
    "gateway": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "LLM gateway",
      "options": { "baseURL": "$BASE/v1", "apiKey": "{env:OPENAI_API_KEY}" },
      "models": { "$m": { "name": "$m" } }
    }
  },
  "model": "gateway/$m",
  "permission": { "edit": "allow", "bash": "allow" }
}
JSON
}

setup_claude() {
  # Skip Claude Code's first-run gates: theme/onboarding, "trust this folder?",
  # and the "Bypass Permissions mode" confirmation. Login is skipped whenever
  # ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN is set.
  mkdir -p "$HOME/.claude"
  cat > "$HOME/.claude.json" <<JSON
{
  "hasCompletedOnboarding": true,
  "theme": "dark",
  "bypassPermissionsModeAccepted": true,
  "projects": { "/workspace": { "hasTrustDialogAccepted": true } }
}
JSON
  cat > "$HOME/.claude/settings.json" <<JSON
{
  "permissions": { "defaultMode": "bypassPermissions" }
}
JSON
  export IS_SANDBOX=1                                  # allows bypass mode even as root
  export DISABLE_AUTOUPDATER=1
  export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
}
