FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl git openssh-client unzip xz-utils procps \
    && rm -rf /var/lib/apt/lists/*

# Node (for claude-code) + bun
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g @anthropic-ai/claude-code @openai/codex opencode-ai \
    && curl -fsSL https://bun.sh/install | BUN_INSTALL=/usr/local bash

RUN useradd -m -s /bin/bash dev && mkdir -p /workspace && chown dev:dev /workspace
COPY entry.sh /usr/local/bin/sandboxd-entry
COPY git-askpass.sh /usr/local/bin/sandboxd-git-askpass
COPY agent-setup.sh /usr/local/bin/sandboxd-agent-setup
RUN chmod +x /usr/local/bin/sandboxd-entry /usr/local/bin/sandboxd-git-askpass /usr/local/bin/sandboxd-agent-setup

USER dev
WORKDIR /workspace
ENV TERM=xterm-256color
CMD ["sleep", "infinity"]
