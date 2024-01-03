#!/bin/bash

set -x

ORG=$1
MSP=$2
NAMESPACE="fabric-$ORG"

source "${BASH_SOURCE%/*}/channel-utils.sh"

CHANNELBLOCK=${CHANNEL}.block
ANCHORTX=./channel-artifacts/${MSP}anchors.tx

pod="$(select_first_cli_pod "$ORG" 0)"

WORKDIR=/opt/blocks/update-anchor-peers-$(date +%s)
echo "WORKDIR=$WORKDIR"

LOCAL_CONFIG_JSON=/tmp/$CHANNELBLOCK.json

pod_exec "$pod" mkdir -p $WORKDIR

"${BASH_SOURCE%/*}/get-channel-config.sh" "$ORG" "$LOCAL_CONFIG_JSON"
if [[ $? -ne 0 ]]; then
    echo "Error: Failed to get channel config" >&2
    exit 1
fi

if ! anchor_peers_set=$(jq -r ".channel_group.groups.Application.groups.${MSP}.values.AnchorPeers.value.anchor_peers | length" ${LOCAL_CONFIG_JSON}); then
    echo "Error: Failed to parse JSON using jq" >&2
    exit 1
fi

if [[ $anchor_peers_set -ne 0 ]]; then
    echo "Anchor peers have already been initialized for $MSP" >&2
    exit 0
fi

# Update the anchor peers
pod_exec "$pod" \
    peer channel update \
    -f "$ANCHORTX" \
    -o "$ORDERER" -c "$CHANNEL" \
    --tls --cafile "$ORDERER_CA"
# TODO: --clientauth --certfile "$CORE_PEER_TLS_CERT_FILE" --keyfile "$CORE_PEER_TLS_KEY_FILE"

if [[ $? -ne 0 ]]; then
    echo "Unable to set anchor peers for $MSP" >&2
    exit 1
fi
