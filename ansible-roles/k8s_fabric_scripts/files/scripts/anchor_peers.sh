#!/bin/bash

set -x

ORG=$1
MSP=$2
NAMESPACE="fabric-$ORG"
POD_SELECTOR=app.kubernetes.io/component=bccli,fabric/organization-index=0

ORDERER=orderer0.luther.systems:7050
CHANNEL=luther
CHANNELBLOCK=luther.block
ANCHORTX=./channel-artifacts/${MSP}anchors.tx
CACERT=orderertls/tlsca.luther.systems-cert.pem

POD=$(kubectl -n "$NAMESPACE" get pods -l "$POD_SELECTOR" -o name | sed 's!^pod/!!')

if [ -z "$POD" ]; then
    echo "Unable to locate pod in namespace $NAMESPACE: $POD_SELECTOR" >&2
    exit 1
fi

kubectl -n "$NAMESPACE" exec "$POD" -- \
    peer channel fetch oldest "$CHANNELBLOCK" -c "$CHANNEL"

if [ -z "$POD" ]; then
    echo "Unable to retrieve latest channel block" >&2
    exit 1
fi

kubectl -n "$NAMESPACE" exec "$POD" -- \
    configtxlator proto_decode --type common.Block --input "$CHANNELBLOCK" --output "$CHANNELBLOCK.json"

if [[ $? -ne 0 ]]; then
    echo "Unable to decode channel block: $CHANNELBLOCK" >&2
    exit 1
fi

# FIXME:  This jq query does not correctly detect anchor peers for $MSP. It
# only works directly after anchor peers have been added.
#
#kubectl -n "$NAMESPACE" exec "$POD" -- \
#    jq -r ".data.data[0].payload.data.config.channel_group.groups.Application.groups.$MSP.values.AnchorPeers.value.anchor_peers[].host" "$CHANNELBLOCK.json"
#
#if [[ $? -eq 0 ]]; then
#    echo "Anchor peers have been initialized" >&2
#    exit 0
#fi

kubectl -n "$NAMESPACE" exec "$POD" -- \
    peer channel update -o "$ORDERER" -c "$CHANNEL" -f "$ANCHORTX" --tls true --cafile "$CACERT"


echo
# FIXME: Because we can't correctly detect above whether the anchor peers have
# already been added the `channel update` transaction may fail.
#
#if [[ $? -ne 0 ]]; then
#    echo "Unable to set anchor peers" >&2
#    exit 1
#fi
