#!/bin/bash

# Takes to channel config json files and computes an "update" which will modify
# the first config to match the second.  The update proposal is written out so
# that it can be distributed for signatures or submitted with update-channel.sh.

set -xeuo pipefail

ORG_NAME=$1
CONFIG_PATH=$2
MODIFIED_PATH=$3
UPDATE_PATH=$4      # In the proposal envelope
PROPOSAL_PATH=$5    # Proposal to be signed

[ -e "$CONFIG_PATH" ]
[ -e "$MODIFIED_PATH" ]
[ ! -e "$UPDATE_PATH" ]
[ ! -e "$PROPOSAL_PATH" ]

NAMESPACE="fabric-$ORG_NAME"

source "${BASH_SOURCE%/*}/channel-utils.sh"

pod="$(select_first_pod "$ORG_NAME" 0)"

WORKDIR=/tmp/compute-channel-update-${RANDOM}
echo "WORKDIR=$WORKDIR"

REMOTE_CONFIG_PATH="$WORKDIR/config.json"
REMOTE_CONFIG_PB="$WORKDIR/config.pb"
REMOTE_MODIFIED_PATH="$WORKDIR/modified_config.json"
REMOTE_MODIFIED_PB="$WORKDIR/modified_config.pb"
REMOTE_UPDATE_PATH="$WORKDIR/config_update.json"
REMOTE_UPDATE_PB="$WORKDIR/config_update.pb"
REMOTE_UPDATE_ENVELOPE_PATH="$WORKDIR/config_update_in_envelope.json"
REMOTE_PROPOSAL_PATH="$WORKDIR/config_update_in_envelope.pb"

pod_exec "$pod" mkdir -p $WORKDIR

kubectl -n "$NAMESPACE" cp "$CONFIG_PATH" "$pod:$REMOTE_CONFIG_PATH"
kubectl -n "$NAMESPACE" cp "$MODIFIED_PATH" "$pod:$REMOTE_MODIFIED_PATH"

# Compute an "update" (diff object) as json.
pod_exec "$pod" configtxlator proto_encode --type common.Config --input "$REMOTE_CONFIG_PATH" --output "$REMOTE_CONFIG_PB"
pod_exec "$pod" configtxlator proto_encode --type common.Config --input "$REMOTE_MODIFIED_PATH" --output "$REMOTE_MODIFIED_PB"
pod_exec "$pod" configtxlator compute_update --channel_id "$CHANNEL" --original "$REMOTE_CONFIG_PB" --updated "$REMOTE_MODIFIED_PB" --output "$REMOTE_UPDATE_PB"
pod_exec "$pod" configtxlator proto_decode --type common.ConfigUpdate --input "$REMOTE_UPDATE_PB" --output "$REMOTE_UPDATE_PATH"

# TRANFORM is a jq template that wraps the update object in an enveleope to
# create a proposal.
set +e
read -r -d '' TRANSFORM <<EOT
{
    "payload": {
        "header":{
            "channel_header":{
                "channel_id": "$CHANNEL",
                "type":2
            }
        },
        "data": {
            "config_update": .
        }
    }
}
EOT
set -e

# Write the proposal JSON out locally for debugging and create the protobuf
# object to be signed.
pod_exec "$pod" jq "$TRANSFORM" "$REMOTE_UPDATE_PATH" > "$UPDATE_PATH"
kubectl -n "$NAMESPACE" cp "$UPDATE_PATH" "$pod:$REMOTE_UPDATE_ENVELOPE_PATH"
pod_exec "$pod" configtxlator proto_encode --type common.Envelope --input "$REMOTE_UPDATE_ENVELOPE_PATH" --output "$REMOTE_PROPOSAL_PATH"
kubectl -n "$NAMESPACE" cp "$pod:$REMOTE_PROPOSAL_PATH" "$PROPOSAL_PATH"
