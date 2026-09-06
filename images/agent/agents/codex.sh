#!/bin/bash
# codex: OpenAI's Codex CLI. Only speaks the Responses API, so a gateway has to serve
# /v1/responses — LiteLLM does. Its default model is deliberately an OpenAI one: LiteLLM's
# Responses→Bedrock translation fails on a tool_choice conflict, so a Claude model here
# usually ends in an error rather than a run.

agent_describe="Codex CLI (@openai/codex)"
agent_model_default="gpt-5-codex"

agent_setup() {
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
      echo 'wire_api = "responses"'
    fi
  } > "$HOME/.codex/config.toml"
  case "${MODEL:-}" in
    claude*) warn "codex with a Claude model: expect a tool_choice error through LiteLLM. Prefer an OpenAI model (SANDBOXD_SANDBOX_ENV_MODEL, or the model field)." ;;
  esac
}

agent_run() {
  say "starting codex${MODEL:+ ($MODEL)}${BASE:+ via $BASE/v1}"
  codex --dangerously-bypass-approvals-and-sandbox "${PROMPT:-}"
}
