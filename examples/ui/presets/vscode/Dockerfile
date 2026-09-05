# code-server (VS Code in the browser) + git. Editor only: no agent tooling.
FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl git openssh-client procps \
    && rm -rf /var/lib/apt/lists/*

# Installs the .deb from the latest GitHub release (code-server.dev/install.sh).
RUN curl -fsSL https://code-server.dev/install.sh | sh \
    && rm -rf /var/lib/apt/lists/* /root/.cache

RUN useradd -m -s /bin/bash dev && mkdir -p /workspace && chown dev:dev /workspace
COPY --chmod=755 entry.sh /usr/local/bin/sandboxd-entry
COPY --chmod=755 git-askpass.sh /usr/local/bin/sandboxd-git-askpass

USER dev
WORKDIR /workspace
ENV TERM=xterm-256color
CMD ["sleep", "infinity"]
