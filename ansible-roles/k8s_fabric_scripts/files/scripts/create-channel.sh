#!/bin/bash

set -x

ORG=org1
NAMESPACE="fabric-$ORG"

source "${BASH_SOURCE%/*}/channel-utils.sh"

CHANNELTX=./channel-artifacts/channel.tx
CHANNELBLOCK=luther.block

pod="$(select_first_pod $ORG 0)"

pod_exec "$pod" \
    peer channel fetch oldest luther.block -c "$CHANNEL" --connTimeout 30s

if [[ $? -eq 0 ]]; then
    echo "Channel previously created/joined" >&2
    kubectl cp "$NAMESPACE"/"$pod":$CHANNELBLOCK $CHANNELBLOCK
    exit $?
fi

pod_exec "$pod" \
    peer channel create -o "$ORDERER" -c "$CHANNEL" -f "$CHANNELTX" --tls true --cafile "$ORDERER_CA" -t 30s

# If you don't join the channel now then bad things could happen if this pod
# were to crash or get rescheduled
pod_exec "$pod" \
    peer channel join -b "$CHANNELBLOCK"

if [[ $? -ne 0 ]]; then
    echo "Failed to create/join channel" >&2
    exit 1
fi

kubectl cp "$NAMESPACE"/"$pod":$CHANNELBLOCK $CHANNELBLOCK
exit $?
