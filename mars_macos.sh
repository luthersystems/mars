#!/bin/bash

set -exo pipefail

# NOTE:  TFENV_CACHE_PATH is deliberately not the same path as tfenv's default
# installation on the host machine.  On macOS mounting that path would download
# linux binaries which would render tfenv unusable outside of the container.
# While brew puts tfenv in a special place that opens up the Linux default of
# ~/.tfenv/versions but we might as well just use our own custom location.
TFENV_CACHE_PATH="$HOME/.mars/tfenv/versions"
DOCKER_IMAGE=luthersystems/mars
END_USER=$(id -u $USER):$(id -g $USER)
DOCKER_WORKDIR=/terraform
PROJECT_PATH=$(pwd)

ENV_VARS=
if [ -n "${TF_LOG+x}" ]; then
    # TF_LOG has been set.  Forward it to the docker env.
    ENV_VARS="-e TF_LOG=$TF_LOG $ENV_VARS"
fi

if [ -z "$(docker ps | grep pinata-sshd)" ]; then
    echo 2>&1 "pinata-sshd not found;  run pinata-ssh-forward"
fi

mkdir -p $TFENV_CACHE_PATH
docker run --rm -it \
    -e USER_ID=$(id -u $USER) \
    -e GROUP_ID=$(id -g $USER) \
    ${ENV_VARS} \
    -v "$TFENV_CACHE_PATH:/opt/tfenv/versions" \
    -v "$PROJECT_PATH:/terraform" \
    -v "$HOME/.aws/:/opt/home/.aws" \
    $(pinata-ssh-mount) \
    $DOCKER_IMAGE "$@"
