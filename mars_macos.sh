#!/bin/bash

set -exo pipefail

# NOTE:  TFENV_CACHE_PATH is deliberately not the same path as tfenv's default
# installation on the host machine.  On macOS mounting that path would download
# linux binaries which would render tfenv unusable outside of the container.
# While brew puts tfenv in a special place that opens up the Linux default of
# ~/.tfenv/versions but we might as well just use our own custom location.
TFENV_CACHE_PATH="$HOME/.mars/tfenv/versions"

# NOTE:  ANSIBLE_INVENTORY_CACHE_MOUNT is a property of the ec2.py configuration
# (in ec2.ini) which defaults to ~/.ansible (/opt/home/.ansible in the
# container).  So it is possible for an inventory configuration to fail by
# changing the property from its default value.
# NOTE2:  Because Docker Desktop on macos is not able to mount unix sockets the
# inventory cache directory is persisted through a docker-native volume,
# ANSIBLE_INVENTORY_CACHE_VOL.
ANSIBLE_INVENTORY_CACHE_VOL=mars_ansible_inventory_cache
ANSIBLE_INVENTORY_CACHE_MOUNT="/opt/home/.ansible"


DOCKER_IMAGE=luthersystems/mars
END_USER=$(id -u $USER):$(id -g $USER)
DOCKER_WORKDIR=/marsproject
PROJECT_PATH=$(pwd)

ENV_VARS=
if [ -n "${TF_LOG+x}" ]; then
    # TF_LOG has been set.  Forward it to the docker env.
    ENV_VARS="-e TF_LOG=$TF_LOG $ENV_VARS"
fi

if [ -z "$(docker ps | grep pinata-sshd)" ]; then
    echo 2>&1 "pinata-sshd not found;  starting..."
    pinata-ssh-forward
fi

DOCKER_TERM_VARS=-i
if [ -t 1 ]; then
    DOCKER_TERM_VARS=-it
fi

docker volume create "$ANSIBLE_INVENTORY_CACHE_VOL"

mkdir -p $TFENV_CACHE_PATH
docker run --rm $DOCKER_TERM_VARS \
    -e USER_ID=$(id -u $USER) \
    -e GROUP_ID=$(id -g $USER) \
    -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY \
    -e AWS_SECURITY_TOKEN -e AWS_SESSION_TOKEN \
    ${ENV_VARS} \
    -v "$ANSIBLE_INVENTORY_CACHE_VOL:$ANSIBLE_INVENTORY_CACHE_MOUNT" \
    -v "$TFENV_CACHE_PATH:/opt/tfenv/versions" \
    -v "$PROJECT_PATH:$DOCKER_WORKDIR" \
    -v "$HOME/.aws/:/opt/home/.aws" \
    $(pinata-ssh-mount) \
    $DOCKER_IMAGE "$@"
