#!/bin/bash
# Entry of the jupyter preset image. Runs in the PTY. Env (all optional):
#   REPO          cloned into the notebook root when set
#   JUPYTER_UI    lab | notebook (default lab)
#   JUPYTER_PORT  port to listen on (default 8888); preview it with this port
#   JUPYTER_ROOT  notebook root (default $HOME)
#   GIT_TOKEN     secret, for private repos
# Auth is off inside the container: the preview cookie is the gate, and a token
# would have nowhere to go in the redirect flow. allow_origin=* because the proxy
# rewrites Host to localhost while the browser's Origin is the preview subdomain.
set -eu
root="${JUPYTER_ROOT:-$HOME}"
if [ -n "${REPO:-}" ]; then
  root="$root/$(basename "$REPO" .git)"
  if [ -n "${GIT_TOKEN:-}" ]; then
    export GIT_ASKPASS=/usr/local/bin/devagents-git-askpass GIT_TERMINAL_PROMPT=0
  fi
  [ -d "$root/.git" ] || git clone --depth 1 "$REPO" "$root"
fi
exec jupyter "${JUPYTER_UI:-lab}" --ip=0.0.0.0 --port="${JUPYTER_PORT:-8888}" --no-browser \
  --ServerApp.root_dir="$root" --IdentityProvider.token= --ServerApp.password= \
  --ServerApp.allow_remote_access=True --ServerApp.allow_origin='*' --ServerApp.trust_xheaders=True
