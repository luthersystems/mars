#!/bin/bash

set -xeuo pipefail

ORG_INDEX=$1
ORG_NAME=$2
BLOCK_PATH=$3
BLOCK_NAME=$(basename "$BLOCK_PATH")

NAMESPACE="fabric-$ORG_NAME"

source "${BASH_SOURCE%/*}/channel-utils.sh"

pod="$(select_first_pod "$ORG_NAME" "$ORG_INDEX")"

WORKDIR=/tmp/join-channel-${RANDOM}
echo "WORKDIR=$WORKDIR"

pod_exec "$pod" mkdir -p $WORKDIR

kubectl -n "$NAMESPACE" cp "$BLOCK_PATH" "$pod:$WORKDIR/$BLOCK_NAME"

pod_exec "$pod" peer channel join -b "$WORKDIR/$BLOCK_NAME"
