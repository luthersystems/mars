#!/bin/bash

set -x

ORG=$1
MSP=$2
NAMESPACE="fabric-$ORG"

source "${BASH_SOURCE%/*}/channel-utils.sh"

CHANNELBLOCK=${CHANNEL}.block
ANCHORTX=./channel-artifacts/${MSP}anchors.tx

pod="$(select_first_cli_pod "$ORG" 0)"

WORKDIR=/opt/blocks/update-anchor-peers-${RANDOM}
echo "WORKDIR=$WORKDIR"

pod_exec "$pod" mkdir -p $WORKDIR

pod_exec "$pod" \
    peer channel fetch oldest "$WORKDIR/$CHANNELBLOCK" -c "$CHANNEL"

if [[ $? -ne 0 ]]; then
    echo "Unable to retrieve latest channel block" >&2
    exit 1
fi

pod_exec "$pod" \
    configtxlator proto_decode --type common.Block --input "$WORKDIR/$CHANNELBLOCK" --output "$WORKDIR/$CHANNELBLOCK.json"

if [[ $? -ne 0 ]]; then
    echo "Unable to decode channel block: $WORKDIR/$CHANNELBLOCK" >&2
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
    peer channel update \
    -f "$ANCHORTX" \
    -o "$ORDERER" -c "$CHANNEL" \
    --tls --cafile "$ORDERER_CA"
# TODO: --clientauth --certfile "$CORE_PEER_TLS_CERT_FILE" --keyfile "$CORE_PEER_TLS_KEY_FILE"

echo
# FIXME: Because we can't correctly detect above whether the anchor peers have
# already been added the `channel update` transaction may fail.
#
#if [[ $? -ne 0 ]]; then
#    echo "Unable to set anchor peers" >&2
#    exit 1
#fi
