#!/bin/bash

set -euxo pipefail

ORG=org1
NAMESPACE="fabric-$ORG"

source "${BASH_SOURCE%/*}/channel-utils.sh"

CHANNELTX=./channel-artifacts/channel.tx
CHANNELBLOCK=${CHANNEL}.block

# CHANNEL_PARTICIPATION=true: the orderers run with channel participation and
# no system channel (always on Fabric 3). They join the channel from its
# genesis block with osnadmin (join-orderers.sh, mars#343); there is no
# system channel for `peer channel create`. The script does nothing on an
# orderer that already serves the channel.
CHANNEL_PARTICIPATION="${CHANNEL_PARTICIPATION:-false}"
if [[ "$CHANNEL_PARTICIPATION" == "true" ]]; then
    "${BASH_SOURCE%/*}/join-orderers.sh"
fi

pod="$(select_first_cli_pod $ORG 0)"

WORKDIR=/opt/blocks/get-channel-block-$(date +%s)
echo "WORKDIR=$WORKDIR"

pod_exec "$pod" mkdir -p $WORKDIR

fetch_oldest() {
    pod_exec "$pod" \
        peer channel fetch oldest "$WORKDIR/$CHANNELBLOCK" \
        -o "$ORDERER" -c "$CHANNEL" \
        "${ORDERER_TLS_ARGS[@]}"
}

if fetch_oldest; then
    echo "Channel previously created" >&2
elif [[ "$CHANNEL_PARTICIPATION" == "true" ]]; then
    # The orderers just joined: Raft may still be electing a leader.
    fetched=false
    for _ in $(seq 1 "${CHANNEL_FETCH_TRIES:-30}"); do
        sleep "${CHANNEL_FETCH_WAIT:-2}"
        if fetch_oldest; then
            fetched=true
            break
        fi
    done
    if [[ "$fetched" != true ]]; then
        echo "The orderers joined $CHANNEL but $ORDERER does not serve it" >&2
        exit 1
    fi
else
    echo "Creating channel" >&2
    pod_exec "$pod" \
         peer channel create -f "$CHANNELTX" \
         -o "$ORDERER" -c "$CHANNEL" \
         "${ORDERER_TLS_ARGS[@]}" \
	 --outputBlock "$WORKDIR/$CHANNELBLOCK"
fi

if pod_exec "$pod" peer channel getinfo -c ${CHANNEL}; then
    echo "Channel previously joined" >&2
else
    echo "Joining channel" >&2
    pod_exec "$pod" peer channel join -b "$WORKDIR/$CHANNELBLOCK"
fi

kubectl cp "$NAMESPACE"/"$pod":"$WORKDIR/$CHANNELBLOCK" $CHANNELBLOCK
exit $?
