#!/bin/bash

set -euxo pipefail

ORG=org1
NAMESPACE="fabric-$ORG"

source "${BASH_SOURCE%/*}/channel-utils.sh"

CHANNELTX=./channel-artifacts/channel.tx
CHANNELBLOCK=${CHANNEL}.block

pod="$(select_first_cli_pod $ORG 0)"

WORKDIR=/opt/blocks/get-channel-block-$(date +%s)
echo "WORKDIR=$WORKDIR"

pod_exec "$pod" mkdir -p $WORKDIR

if pod_exec "$pod" \
     peer channel fetch oldest "$WORKDIR/$CHANNELBLOCK" \
     -o "$ORDERER" -c "$CHANNEL" \
     --tls --cafile "$ORDERER_CA"; then
     # TODO: --clientauth --certfile "$CORE_PEER_TLS_CERT_FILE" --keyfile "$CORE_PEER_TLS_KEY_FILE"
    echo "Channel previously created" >&2
else
    echo "Creating channel" >&2
    # TODO: --clientauth --certfile "$CORE_PEER_TLS_CERT_FILE" --keyfile "$CORE_PEER_TLS_KEY_FILE"
    pod_exec "$pod" \
         peer channel create -f "$CHANNELTX" \
         -o "$ORDERER" -c "$CHANNEL" \
         --tls --cafile "$ORDERER_CA" \
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
