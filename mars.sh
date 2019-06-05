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
ANSIBLE_INVENTORY_CACHE_PATH="$HOME/.ansible"
ANSIBLE_INVENTORY_CACHE_MOUNT="/opt/home/.ansible"

DOCKER_IMAGE=luthersystems/mars
END_USER=$(id -u $USER):$(id -g $USER)
DOCKER_WORKDIR=/marsproject
PROJECT_PATH=$(pwd)

mkdir -p $TFENV_CACHE_PATH
docker run --rm -it \
    -u $END_USER \
    -v "$ANSIBLE_INVENTORY_CACHE_PATH:$ANSIBLE_INVENTORY_CACHE_MOUNT" \
    -v "$TFENV_CACHE_PATH:/opt/tfenv/versions" \
    -v "$PROJECT_PATH:$DOCKER_WORKDIR" \
    -v "$HOME/.aws/:/opt/home/.aws" \
    -v "$SSH_AUTH_SOCK:$SSH_AUTH_SOCK" \
    -e "SSH_AUTH_SOCK=$SSH_AUTH_SOCK" \
    -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY \
    -e AWS_SECURITY_TOKEN -e AWS_SESSION_TOKEN \
    $DOCKER_IMAGE $@
