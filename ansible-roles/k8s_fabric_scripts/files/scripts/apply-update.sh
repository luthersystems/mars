#!/bin/bash

# NOTE:  Before applying an update it must be signed (M-1) orgs where M
# satisfies the channel mod_policy (default is MAJORITY).  After these
# signatures are collected apply the update with a *different* org to reach the
# requisite M signatures.

set -xeuo pipefail

ORG_NAME=$1
UPDATE_PB_PATH=$2
UPDATE_PB_NAME=$(basename "$UPDATE_PB_PATH")

NAMESPACE="fabric-$ORG_NAME"

source "${BASH_SOURCE%/*}/channel-utils.sh"

pod="$(select_first_pod "$ORG_NAME" 0)"

WORKDIR=/tmp/apply-update-${RANDOM}
echo "WORKDIR=$WORKDIR"

pod_exec "$pod" mkdir -p $WORKDIR

kubectl -n "$NAMESPACE" cp "$UPDATE_PB_PATH" "$pod:$WORKDIR/$UPDATE_PB_NAME"

pod_exec "$pod" peer channel update -f "$WORKDIR/$UPDATE_PB_NAME" -c "$CHANNEL" -o "$ORDERER" --tls --cafile $ORDERER_CA
