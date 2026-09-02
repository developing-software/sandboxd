#!/bin/sh
# GIT_ASKPASS helper: answers git's credential prompts from $GIT_TOKEN.
case "$1" in
  *sername*) echo "${GIT_USERNAME:-x-access-token}" ;;
  *) echo "$GIT_TOKEN" ;;
esac
