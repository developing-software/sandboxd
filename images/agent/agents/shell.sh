#!/bin/bash
# shell: no agent at all. The clone, the branch and SETUP still run; you land in bash.
# Also what an empty PROMPT resolves to, so "just give me the repo" needs no `agent` field.

# This file is sourced by the entry, so shellcheck cannot see who reads these two.
# shellcheck disable=SC2034
agent_describe="an interactive shell, no agent"
agent_model_default=""

agent_run() {
  say "agent=shell; dropping to a shell."
}
