#!/bin/bash

set -x

ORG=$1
NAMESPACE="fabric-$ORG"
CHANNEL=luther
CHANNELBLOCK="$CHANNEL.block"

if [ ! -f "$CHANNELBLOCK" ]; then
    echo "Channel block does not exist: $CHANNELBLOCK" >&2
    exit 1
fi

POD_SELECTOR=app.kubernetes.io/component=bccli

PODS=$(kubectl -n "$NAMESPACE" get pods -l "$POD_SELECTOR" -o name | sed 's!^pod/!!')

for POD in $PODS; do
    kubectl cp "$CHANNELBLOCK" "$NAMESPACE"/"$POD":"$CHANNELBLOCK"
    if [ $? -ne 0 ]; then
        echo "Unable to synchronize $CHANNELBLOCK to pod: $NAMESPACE/$POD" >&2
        exit 1
    fi
done

for POD in $PODS; do
    kubectl -n "$NAMESPACE" exec "$POD" -- \
        peer channel fetch newest -c "$CHANNEL"

    if [[ $? -ne 0 ]]; then
        kubectl -n "$NAMESPACE" exec "$POD" -- \
            peer channel join -b "$CHANNELBLOCK"
        if [[ $? -ne 0 ]]; then
            echo "Unable to join channel" >&2
            exit 1
        fi
    fi
done

echo "$ORG peers have joined the channel: $CHANNEL" >&2
