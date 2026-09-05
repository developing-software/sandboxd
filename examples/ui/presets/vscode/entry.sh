#!/bin/bash
# Entry of the vscode preset image. Runs in the PTY. Env (all optional):
#   REPO         cloned into /workspace/<name> when set; the editor opens it
#   VSCODE_PORT  port code-server listens on (default 8080); preview it with this port
#   GIT_TOKEN    secret, for private repos / push
# Auth is off inside the container: the preview cookie is the gate. The preview
# proxy forwards x-forwarded-host, which code-server checks Origin against;
# --trusted-origins '*' covers proxies that do not.
set -u
ws=/workspace
if [ -n "${GIT_TOKEN:-}" ]; then
  export GIT_ASKPASS=/usr/local/bin/sandboxd-git-askpass GIT_TERMINAL_PROMPT=0
fi
git config --global user.name  "${GIT_AUTHOR_NAME:-sandboxd}"
git config --global user.email "${GIT_AUTHOR_EMAIL:-sandboxd@localhost}"
if [ -n "${REPO:-}" ]; then
  ws="/workspace/$(basename "$REPO" .git)"
  if [ ! -d "$ws/.git" ] && ! git clone "$REPO" "$ws" 2>&1; then
    echo "▶ clone failed; dropping to a shell so you can see why."
    exec bash -l
  fi
fi
echo "▶ code-server on :${VSCODE_PORT:-8080} · $ws"
exec code-server --auth none --bind-addr "0.0.0.0:${VSCODE_PORT:-8080}" \
  --disable-telemetry --disable-update-check --disable-workspace-trust \
  --trusted-origins '*' "$ws"
