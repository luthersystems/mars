#!/bin/bash

set -eo pipefail

DEV_MOUNTS=''
if [[ "$MARS_DEV" == "true" ]]; then
    MARS_DEV_ROOT="$(dirname $(greadlink -f $0))"
    DEV_MOUNTS="-v ${MARS_DEV_ROOT}/scripts:/opt/mars:ro \
                -v ${MARS_DEV_ROOT}/ansible-roles:/opt/ansible/roles:ro \
                -v ${MARS_DEV_ROOT}/ansible-plugins:/opt/ansible/plugins:ro"
fi

if [[ "$MARS_DEBUG" == "true" ]]; then
    set -x
fi

fullpath() {
    cd "$1" && pwd
}

getroot() {
    GIT_ROOT=$(git rev-parse --show-toplevel 2>/dev/null)
    if [ -z "$PROJECT_PATH"]; then
        if [ -n "$GIT_ROOT" ]; then
            PROJECT_PATH="$GIT_ROOT"
        fi
    fi
}

# NOTE:  TFENV_CACHE_PATH is deliberately not the same path as tfenv's default
# installation on the host machine.  On macOS mounting that path would download
# linux binaries which would render tfenv unusable outside of the container.
# While brew puts tfenv in a special place that opens up the Linux default of
# ~/.tfenv/versions but we might as well just use our own custom location.
TFENV_CACHE_PATH="$HOME/.mars/tfenv/versions"

# Cache directory for provider plugins
TF_PLUGIN_CACHE_DIR="$HOME/.mars/tf-plugin-cache"

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
DOCKER_PROJECT_PATH=/marsproject
getroot
PROJECT_PATH=$(fullpath ${PROJECT_PATH:-$(pwd)})
WORK_REL_PATH="${PWD#$PROJECT_PATH}"  # Includes leading dir separator
DOCKER_WORK_DIR="$DOCKER_PROJECT_PATH$WORK_REL_PATH"

MARS_VERSION=latest
if [ -f "$PROJECT_PATH/.mars-version" ]; then
    MARS_VERSION=$(cat $PROJECT_PATH/.mars-version)
elif [ -f "$GIT_ROOT/.mars-version" ]; then
    MARS_VERSION=$(cat $GIT_ROOT/.mars-version)
fi

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
if [ -t 1 -a ! -p /dev/stdin ]; then
    DOCKER_TERM_VARS=-it
fi

docker volume create "$ANSIBLE_INVENTORY_CACHE_VOL" >/dev/null

SHELL_OPTS=
if [[ "$MARS_SHELL" == "true" ]]; then
    SHELL_OPTS="--entrypoint /bin/bash"
fi

mkdir -p $TFENV_CACHE_PATH
mkdir -p $TF_PLUGIN_CACHE_DIR
docker run --rm $DOCKER_TERM_VARS \
    -e USER_ID=$(id -u $USER) \
    -e GROUP_ID=$(id -g $USER) \
    -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY \
    -e AWS_SECURITY_TOKEN -e AWS_SESSION_TOKEN \
    -e TF_PLUGIN_CACHE_DIR=/opt/tf-plugin-cache-dir \
    $ENV_VARS \
    $DEV_MOUNTS \
    -v "$ANSIBLE_INVENTORY_CACHE_VOL:$ANSIBLE_INVENTORY_CACHE_MOUNT" \
    -v "$TFENV_CACHE_PATH:/opt/tfenv/versions" \
    -v "$TF_PLUGIN_CACHE_DIR:/opt/tf-plugin-cache-dir" \
    -v "$HOME/.aws/:/opt/home/.aws" \
    -v "$HOME/.azure/:/opt/home/.azure" \
    -v "$PROJECT_PATH:$DOCKER_PROJECT_PATH" \
    -w "$DOCKER_WORK_DIR" \
    -e ANSIBLE_LOAD_CALLBACK_PLUGINS=yes \
    -e ANSIBLE_STDOUT_CALLBACK=yaml \
    $SHELL_OPTS \
    $(pinata-ssh-mount) \
    "$DOCKER_IMAGE:$MARS_VERSION" "$@"
