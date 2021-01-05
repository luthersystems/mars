#!/bin/bash

# Retrieves the latest channel config and saves it as a json object.  Useful
# for debugging. The config can also be manipulated and used to update the
# channel config using compute-channel-update.sh.

set -xeuo pipefail

ORG_NAME=$1
CONFIG_PATH=$2
BLOCK_NAME=channel_config.pb
BLOCK_JSON=channel_config.json

[ ! -e "$CONFIG_PATH" ]

NAMESPACE="fabric-$ORG_NAME"

source "${BASH_SOURCE%/*}/channel-utils.sh"

pod="$(select_first_pod "$ORG_NAME" 0)"

WORKDIR=/tmp/get-channel-config-${RANDOM}
echo "WORKDIR=$WORKDIR"

REMOTE_BLOCK_PATH="$WORKDIR/channel_config.pb"
REMOTE_BLOCK_JSON="$WORKDIR/channel_config.json"

pod_exec "$pod" mkdir -p $WORKDIR

pod_exec "$pod" peer channel fetch config "$REMOTE_BLOCK_PATH" -o "$ORDERER" -c "$CHANNEL" --tls --cafile "$ORDERER_CA"

pod_exec "$pod" configtxlator proto_decode --input "$REMOTE_BLOCK_PATH" --output "$REMOTE_BLOCK_JSON" --type common.Block

pod_exec "$pod" jq .data.data[0].payload.data.config "$REMOTE_BLOCK_JSON" > "$CONFIG_PATH"
