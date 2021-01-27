#!/bin/bash

set -euxo pipefail

CC_NAME="$1"
CC_VERSION="$2"
ENDORSEMENT_POLICY="$3"
SEQ_NO="$4"
ORG="$5"
shift 5
ORG_DOMAINS="$@"
NAMESPACE="fabric-$ORG"

source "${BASH_SOURCE%/*}/channel-utils.sh"

pod="$(select_first_pod "$ORG" 0)"

org_peer() {
    local org_domain="$1"
    local root=/etc/hyperledger/fabric/tls/all-cas
    pod_exec "$pod" sh -c "ls '${root}/${org_domain}/peers/' | shuf | head -n 1 | xargs basename"
}

peer_address() {
    echo "$1:7051"
}

ca_path() {
    local org_domain="$1"
    local peer="$2"
    local root=/etc/hyperledger/fabric/tls/all-cas

    echo "${root}/${org_domain}/peers/${peer}/tls/ca.crt"
}

peer_args() {
    local peer_args=""
    for org in $ORG_DOMAINS; do
        local peer="$(org_peer "$org")"
        if [ -z "$peer" ]; then
            echo "peer not found for org: $org" >&2
            exit 1
        fi
        peer_args+=" --peerAddresses $(peer_address "$peer")"
        peer_args+=" --tlsRootCertFiles $(ca_path "$org" "$peer")"
    done
    echo "$peer_args"
}

ARGS="$(peer_args)"

if [ -z "$ARGS" ]; then
    exit 1
fi

if ! pod_exec "$pod" peer lifecycle chaincode commit \
     $ARGS \
     --channelID "$CHANNEL" --tls \
     --cafile "$ORDERER_CA" --orderer "$ORDERER" \
     --name "$CC_NAME" --version "$CC_VERSION" \
     --collections-config "$COLLECTIONS_PATH" \
     --signature-policy "$ENDORSEMENT_POLICY" \
     --sequence "$SEQ_NO"
then
    echo "Failed to commit chaincode lifecycle" >&2
    exit 1
fi
