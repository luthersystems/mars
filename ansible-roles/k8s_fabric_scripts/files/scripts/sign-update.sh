#!/bin/bash

# NOTE:  This replaces the given update.pb file with a new file containing a
# signature from the specified org.

set -xeuo pipefail

ORG_NAME=$1
UPDATE_PB_PATH=$2
UPDATE_PB_NAME=$(basename "$UPDATE_PB_PATH")

NAMESPACE="fabric-$ORG_NAME"

source "${BASH_SOURCE%/*}/channel-utils.sh"

pod="$(select_first_cli_pod "$ORG_NAME" 0)"

WORKDIR=/opt/blocks/apply-update-$(date +%s)
echo "WORKDIR=$WORKDIR"
pod_exec "$pod" mkdir -p $WORKDIR

kubectl -n "$NAMESPACE" cp "$UPDATE_PB_PATH" "$pod:$WORKDIR/$UPDATE_PB_NAME"

pod_exec "$pod" peer channel signconfigtx -f "$WORKDIR/$UPDATE_PB_NAME"

kubectl -n "$NAMESPACE" cp "$pod:$WORKDIR/$UPDATE_PB_NAME" "$UPDATE_PB_PATH"
