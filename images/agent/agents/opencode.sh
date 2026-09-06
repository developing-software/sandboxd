#!/bin/bash
# opencode: sst's OpenCode. Reaches a gateway through the openai-compatible provider, so
# the model has to be declared in the config before it can be selected — which is why this
# file writes one model rather than a catalogue.

agent_describe="OpenCode (opencode-ai)"
agent_model_default="gpt-4o"

agent_setup() {
  [ -z "$BASE" ] && return 0
  mkdir -p "$HOME/.config/opencode"
  cat > "$HOME/.config/opencode/opencode.json" <<JSON
{
  "\$schema": "https://opencode.ai/config.json",
  "provider": {
    "gateway": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "LLM gateway",
      "options": { "baseURL": "$BASE/v1", "apiKey": "{env:OPENAI_API_KEY}" },
      "models": { "$MODEL": { "name": "$MODEL" } }
    }
  },
  "model": "gateway/$MODEL",
  "permission": { "edit": "allow", "bash": "allow" }
}
JSON
}

agent_run() {
  say "starting opencode${MODEL:+ ($MODEL)}${BASE:+ via $BASE/v1}"
  opencode --prompt "${PROMPT:-}"
}
