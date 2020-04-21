#!/bin/bash

set -xeuo pipefail

ORG_NAME=$1
BLOCK_PATH=$2
BLOCK_NAME=$(basename "$BLOCK_PATH")

NAMESPACE="fabric-$ORG_NAME"

source "${BASH_SOURCE%/*}/channel-config.sh"

pod_select "$ORG_NAME" 0

WORKDIR=/tmp/get-channel-block-${RANDOM}
echo "WORKDIR=$WORKDIR"

pod_exec mkdir -p $WORKDIR

pod_exec peer channel fetch 0 "$WORKDIR/$BLOCK_NAME" -o "$ORDERER" -c "$CHANNEL" --tls --cafile "$ORDERER_CA"

kubectl -n "$NAMESPACE" cp "$POD:$WORKDIR/$BLOCK_NAME" "$BLOCK_PATH"
