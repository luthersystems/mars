#!/bin/bash

set -x

ORG=org1
NAMESPACE="fabric-$ORG"
POD_SELECTOR=app.kubernetes.io/component=bccli,fabric/organization-index=0

ORDERER=orderer0.luther.systems:7050
CHANNEL=luther
CHANNELTX=./channel-artifacts/channel.tx
CHANNELBLOCK=luther.block
CACERT=orderertls/tlsca.luther.systems-cert.pem

POD=$(kubectl -n "$NAMESPACE" get pods -l "$POD_SELECTOR" -o name | head -n 1 | sed 's!^pod/!!')

if [ -z "$POD" ]; then
    echo "Unable to find pod matching selector: $POD_SELECTOR$" >&2
    exit 1
fi

kubectl -n "$NAMESPACE" exec "$POD" -- \
    peer channel fetch oldest luther.block -c "$CHANNEL"

if [[ $? -eq 0 ]]; then
    echo "Channel previously created/joined" >&2
    kubectl cp "$NAMESPACE"/"$POD":$CHANNELBLOCK $CHANNELBLOCK
    exit $?
fi

kubectl -n "$NAMESPACE" exec "$POD" -- \
    peer channel create -o "$ORDERER" -c "$CHANNEL" -f "$CHANNELTX" --tls true --cafile "$CACERT"

# If you don't join the channel now then bad things could happen if this pod
# were to crash or get rescheduled
kubectl -n "$NAMESPACE" exec "$POD" -- \
    peer channel join -b "$CHANNELBLOCK"

if [[ $? -ne 0 ]]; then
    echo "Failed to create/join channel" >&2
    exit 1
fi

kubectl cp "$NAMESPACE"/"$POD":$CHANNELBLOCK $CHANNELBLOCK
exit $?
