#!/bin/bash

set -xeuo pipefail

ORG_INDEX=$1
ORG_NAME=$2
BLOCK_PATH=$3
BLOCK_NAME=$(basename "$BLOCK_PATH")

NAMESPACE="fabric-$ORG_NAME"

source "${BASH_SOURCE%/*}/channel-config.sh"

pod_select "$ORG_NAME" "$ORG_INDEX"

WORKDIR=/tmp/join-channel-${RANDOM}
echo "WORKDIR=$WORKDIR"

pod_exec mkdir -p $WORKDIR

kubectl -n "$NAMESPACE" cp "$BLOCK_PATH" "$POD:$WORKDIR/$BLOCK_NAME"

pod_exec peer channel join -b "$WORKDIR/$BLOCK_NAME"
