#!/bin/bash

# NOTE:  This replaces the given update.pb file with a new file containing a
# signature from the specified org.

set -xeuo pipefail

ORG_NAME=$1
UPDATE_PB_PATH=$2
UPDATE_PB_NAME=$(basename "$UPDATE_PB_PATH")

NAMESPACE="fabric-$ORG_NAME"

source "${BASH_SOURCE%/*}/channel-config.sh"

pod_select "$ORG_NAME" 0

WORKDIR=/tmp/apply-update-${RANDOM}
echo "WORKDIR=$WORKDIR"

pod_exec mkdir -p $WORKDIR

kubectl -n "$NAMESPACE" cp "$UPDATE_PB_PATH" "$POD:$WORKDIR/$UPDATE_PB_NAME"

pod_exec peer channel signconfigtx -f "$WORKDIR/$UPDATE_PB_NAME"

kubectl -n "$NAMESPACE" cp "$POD:$WORKDIR/$UPDATE_PB_NAME" "$UPDATE_PB_PATH"
