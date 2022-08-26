#!/bin/bash

set -xeuo pipefail

ORG_NAME=$1
BLOCK_PATH=$2
BLOCK_NAME=$(basename "$BLOCK_PATH")

NAMESPACE="fabric-$ORG_NAME"

source "${BASH_SOURCE%/*}/channel-utils.sh"

pod="$(select_first_cli_pod "$ORG_NAME" 0)"

WORKDIR=/opt/blocks/get-channel-block-$(date +%s)
echo "WORKDIR=$WORKDIR"

pod_exec "$pod" mkdir -p $WORKDIR

if [[ $? -ne 0 ]]; then
    echo "Unable to create block dir" >&2
    exit 1
fi

pod_exec "$pod" \
    peer channel fetch oldest "$WORKDIR/$BLOCK_NAME" -c "$CHANNEL"

if [[ $? -ne 0 ]]; then
    echo "Unable to retrieve latest channel block" >&2
    exit 1
fi

if ! pod_fetch "$pod" "$WORKDIR/$BLOCK_NAME" "$BLOCK_PATH"; then
    echo "Unable to copy channel block" >&2
    exit 1
fi
