#!/bin/bash

set -x

ORG=$1
MSP=$2
NAMESPACE="fabric-$ORG"

source "${BASH_SOURCE%/*}/channel-utils.sh"

CHANNELBLOCK=luther.block
ANCHORTX=./channel-artifacts/${MSP}anchors.tx

pod="$(select_first_pod "$ORG" 0)"

pod_exec "$pod" \
    peer channel fetch oldest "$CHANNELBLOCK" -c "$CHANNEL"

if [[ $? -ne 0 ]]; then
    echo "Unable to retrieve latest channel block" >&2
    exit 1
fi

pod_exec "$pod" \
    configtxlator proto_decode --type common.Block --input "$CHANNELBLOCK" --output "$CHANNELBLOCK.json"

if [[ $? -ne 0 ]]; then
    echo "Unable to decode channel block: $CHANNELBLOCK" >&2
    exit 1
fi

# FIXME:  This jq query does not correctly detect anchor peers for $MSP. It
# only works directly after anchor peers have been added.
#
#pod_exec "$pod" \
#    jq -r ".data.data[0].payload.data.config.channel_group.groups.Application.groups.$MSP.values.AnchorPeers.value.anchor_peers[].host" "$CHANNELBLOCK.json"
#
#if [[ $? -eq 0 ]]; then
#    echo "Anchor peers have been initialized" >&2
#    exit 0
#fi

pod_exec "$pod" \
    peer channel update -o "$ORDERER" -c "$CHANNEL" -f "$ANCHORTX" --tls true --cafile "$ORDERER_CA"


echo
# FIXME: Because we can't correctly detect above whether the anchor peers have
# already been added the `channel update` transaction may fail.
#
#if [[ $? -ne 0 ]]; then
#    echo "Unable to set anchor peers" >&2
#    exit 1
#fi
