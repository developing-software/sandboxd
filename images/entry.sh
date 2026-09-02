#!/bin/bash
# Runs inside the sandbox PTY. Env from the daemon: REPO BRANCH BASE_BRANCH PROMPT
# and optionally GIT_TOKEN ANTHROPIC_API_KEY. Ends in a shell no matter what,
# so the user can inspect failures or `git push` when the agent is done.
set -u
cd /workspace

if [ -n "${GIT_TOKEN:-}" ]; then
  export GIT_ASKPASS=/usr/local/bin/cp-git-askpass
  export GIT_TERMINAL_PROMPT=0
fi
git config --global user.name  "${GIT_AUTHOR_NAME:-cp-agent}"
git config --global user.email "${GIT_AUTHOR_EMAIL:-cp-agent@localhost}"
git config --global init.defaultBranch main

echo "▶ cloning $REPO"
if git clone ${BASE_BRANCH:+--branch "$BASE_BRANCH"} "$REPO" . 2>&1; then
  git checkout -b "$BRANCH" 2>&1
  echo "▶ on branch $BRANCH"
  if [ -z "${PROMPT:-}" ]; then
    echo "▶ no prompt given; dropping to a shell in the cloned repo."
  elif command -v claude >/dev/null 2>&1; then
    echo "▶ starting claude"
    claude --dangerously-skip-permissions "$PROMPT"
    echo
    echo "▶ claude exited. You are in a shell; \`git push -u origin $BRANCH\` when ready."
  else
    echo "▶ claude is not installed in this image; dropping to a shell."
  fi
else
  echo "▶ clone failed; dropping to a shell so you can see why."
fi
exec bash -l
