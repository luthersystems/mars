#!/bin/bash

DOCKER_IMAGE=luthersystems/mars
END_USER=$(id -u $USER):$(id -g $USER)
DOCKER_WORKDIR=/terraform

PROJECT_PATH=$(pwd)
docker run --rm -it \
    -u $END_USER \
    -v "$PROJECT_PATH:/terraform" \
    -v "$HOME/.aws/:/opt/home/.aws" \
    -v "$SSH_AUTH_SOCK:$SSH_AUTH_SOCK" \
    -e "SSH_AUTH_SOCK=$SSH_AUTH_SOCK" \
    $DOCKER_IMAGE $@


