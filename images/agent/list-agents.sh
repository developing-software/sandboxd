#!/bin/bash
# `sandboxd-agents` — what this image can run. The agents dir is the list, so this reads
# the same folder the entry does rather than a table someone has to remember to update.
#   sandboxd-agents           one line each, with the default model
#   sandboxd-agents --names   just the names, for scripts
set -u
dir="${SANDBOXD_AGENTS_DIR:-/opt/sandboxd/agents}"

for file in "$dir"/*.sh; do
  [ -f "$file" ] || continue
  name="$(basename "$file" .sh)"
  case "$name" in _*) continue ;; esac
  if [ "${1:-}" = "--names" ]; then
    echo "$name"
    continue
  fi
  agent_describe=""
  agent_model_default=""
  # Sourced in a subshell: this is a listing, and an agent file may export whatever it likes.
  eval "$(
    # shellcheck source=/dev/null
    . "$file" 2>/dev/null
    printf 'agent_describe=%q; agent_model_default=%q' "$agent_describe" "$agent_model_default"
  )"
  printf '%-10s %-42s %s\n' "$name" "$agent_describe" "${agent_model_default:+model: $agent_model_default}"
done
