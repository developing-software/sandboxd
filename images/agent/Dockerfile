# sandboxd-agent: a repo, a branch, and one coding agent in the PTY.
# The semantics of this image live in entry.sh and agents/; the control plane knows only
# image + cmd + env (DESIGN.md decision 20). Everything installed here exists so an agent
# can read a repo, change it, and push a branch.
FROM debian:bookworm-slim

# Pinned at build time so a published tag is reproducible. CI passes the versions it
# resolved; `latest` is the local-build convenience.
ARG CLAUDE_CODE_VERSION=latest
ARG CODEX_VERSION=latest
ARG OPENCODE_VERSION=latest
ARG NODE_MAJOR=22

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl git gnupg openssh-client unzip xz-utils procps \
      jq less ripgrep nano tini \
    && rm -rf /var/lib/apt/lists/*

# gh: an agent that pushes a branch is one step from opening the pull request.
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
      -o /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
      > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update && apt-get install -y --no-install-recommends gh \
    && rm -rf /var/lib/apt/lists/*

# Node for the three agent CLIs; bun because most repos an agent is pointed at want it.
RUN curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g \
         "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
         "@openai/codex@${CODEX_VERSION}" \
         "opencode-ai@${OPENCODE_VERSION}" \
    && npm cache clean --force \
    && curl -fsSL https://bun.sh/install | BUN_INSTALL=/usr/local bash

# The agents folder is the image's contract: one file per agent, discovered at start-up.
# Mount over /opt/sandboxd/agents (or set SANDBOXD_AGENTS_DIR) to add one without rebuilding.
COPY agents/ /opt/sandboxd/agents/
COPY entry.sh /usr/local/bin/sandboxd-entry
COPY git-askpass.sh /usr/local/bin/sandboxd-git-askpass
COPY list-agents.sh /usr/local/bin/sandboxd-agents
RUN chmod +x /usr/local/bin/sandboxd-entry /usr/local/bin/sandboxd-git-askpass \
             /usr/local/bin/sandboxd-agents \
    && useradd -m -s /bin/bash dev \
    && mkdir -p /workspace && chown dev:dev /workspace

USER dev
WORKDIR /workspace
ENV TERM=xterm-256color \
    SANDBOXD_AGENTS_DIR=/opt/sandboxd/agents
# The daemon execs the preset's cmd into a PTY of its own; the container itself just stays up.
CMD ["sleep", "infinity"]

LABEL org.opencontainers.image.title="sandboxd-agent" \
      org.opencontainers.image.description="A repo on a fresh branch, handed to Claude Code, Codex, OpenCode or a shell." \
      org.opencontainers.image.source="https://github.com/developing-software/sandboxd" \
      org.opencontainers.image.documentation="https://github.com/developing-software/sandboxd/blob/main/images/README.md" \
      org.opencontainers.image.licenses="MIT"
