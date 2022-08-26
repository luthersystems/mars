#!/bin/bash

set -x

ORG=$1
NAMESPACE="fabric-$ORG"

source "${BASH_SOURCE%/*}/channel-utils.sh"

CHANNELBLOCK=${CHANNEL}.block

WORKDIR=/opt/blocks/sync-channel-block-$(date +%s)

if [ ! -f "$CHANNELBLOCK" ]; then
    echo "Channel block does not exist: $CHANNELBLOCK" >&2
    exit 1
fi

pods="$(select_cli_pods $ORG)"

for pod in $pods; do
    pod_exec "$pod" mkdir -p $WORKDIR
    pod_copy "$pod" "$CHANNELBLOCK" "$WORKDIR/$CHANNELBLOCK"
    if [ $? -ne 0 ]; then
        echo "Unable to synchronize $CHANNELBLOCK to pod: $NAMESPACE/$pod" >&2
        exit 1
    fi
done

for pod in $pods; do
    pod_exec "$pod" \
        peer channel fetch newest "$WORKDIR/$CHANNELBLOCK" -c "$CHANNEL"

    if [[ $? -ne 0 ]]; then
        pod_exec "$pod" \
            peer channel join -b"$WORKDIR/$CHANNELBLOCK"
        if [[ $? -ne 0 ]]; then
            echo "Unable to join channel" >&2
            exit 1
        fi
    fi
done

echo "$ORG peers have joined the channel: $CHANNEL" >&2
