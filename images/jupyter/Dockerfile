# JupyterLab on the stock docker-stacks image, plus the sandboxd entry.
# The daemon execs /usr/local/bin/sandboxd-entry as the image's default user (jovyan).
FROM quay.io/jupyter/minimal-notebook:latest
USER root
COPY --chmod=755 entry.sh /usr/local/bin/sandboxd-entry
COPY --chmod=755 git-askpass.sh /usr/local/bin/sandboxd-git-askpass
USER ${NB_UID}

LABEL org.opencontainers.image.title="sandboxd-jupyter" \
      org.opencontainers.image.description="JupyterLab or classic Notebook on a repo, behind the sandboxd preview proxy." \
      org.opencontainers.image.source="https://github.com/developing-software/sandboxd" \
      org.opencontainers.image.documentation="https://github.com/developing-software/sandboxd/blob/main/images/README.md" \
      org.opencontainers.image.licenses="MIT"
