#!/bin/bash
# Sourced by sandboxd-entry before any agent file. Leading underscore = library, not an
# agent: discovery skips it. Maps the one gateway tuple (LLM_BASE_URL, LLM_API_KEY, MODEL)
# onto the env every harness reads, so an agent file only writes what is its own.
#
# Exports for agent files: BASE (gateway root, no trailing /v1; empty = the vendor default).

BASE="${LLM_BASE_URL:-}"
BASE="${BASE%/}"
BASE="${BASE%/v1}"
export BASE

if [ -n "$BASE" ]; then
  export OPENAI_BASE_URL="$BASE/v1"
  export ANTHROPIC_BASE_URL="$BASE"
fi
if [ -n "${LLM_API_KEY:-}" ]; then
  export OPENAI_API_KEY="$LLM_API_KEY"
  # Claude Code sends ANTHROPIC_AUTH_TOKEN as a Bearer token, which is what LiteLLM expects.
  [ -z "${ANTHROPIC_API_KEY:-}" ] && export ANTHROPIC_AUTH_TOKEN="$LLM_API_KEY"
fi

say() { printf '▶ %s\n' "$*"; }
warn() { printf '⚠ %s\n' "$*" >&2; }
