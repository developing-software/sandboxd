# JupyterLab on the stock docker-stacks image, plus the devagents entry.
# The daemon execs /usr/local/bin/devagents-entry as the image's default user (jovyan).
FROM quay.io/jupyter/minimal-notebook:latest
USER root
COPY --chmod=755 entry.sh /usr/local/bin/devagents-entry
COPY --chmod=755 git-askpass.sh /usr/local/bin/devagents-git-askpass
USER ${NB_UID}
