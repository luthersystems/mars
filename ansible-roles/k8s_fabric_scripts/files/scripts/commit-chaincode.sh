#!/bin/bash

set -euxo pipefail

CC_NAME="$1"
CC_VERSION="$2"
ENDORSEMENT_POLICY="$3"
SEQ_NO="$4"
DOMAIN="$5"
shift 5
ORG="$1"
ORGS="$@"
NAMESPACE="fabric-$ORG"

source "${BASH_SOURCE%/*}/channel-utils.sh"

pod="$(select_first_pod "$ORG" 0)"

peer_address() {
    org=$1

    echo "peer0.${org}.${DOMAIN}:7051"
}

ca_path() {
    org=$1
    local root=/etc/hyperledger/fabric/tls/all-cas

    echo "${root}/${org}.${DOMAIN}/peers/peer0.${org}.${DOMAIN}/tls/ca.crt"
}

peer_args() {
    local peer_args=""
    for org in $ORGS; do
        peer_args+=" --peerAddresses $(peer_address "$org")"
        peer_args+=" --tlsRootCertFiles $(ca_path "$org")"
    done
    echo "$peer_args"
}

if ! pod_exec "$pod" peer lifecycle chaincode commit \
     $(peer_args) \
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
