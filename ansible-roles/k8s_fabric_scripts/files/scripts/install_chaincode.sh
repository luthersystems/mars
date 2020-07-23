#!/bin/bash

set -x

ORG=$1
CHAINCODE=$2
VERSION=$3
NAMESPACE="fabric-$ORG"

source "${BASH_SOURCE%/*}/channel-utils.sh"

CHAINCODEPATH=/opt/gopath/src/github.com/hyperledger/fabric/examples/chaincode/go/$CHAINCODE-$VERSION.cds

pods="$(select_pods "$ORG")"

for pod in $pods; do
    # Determine if the exact version of the specified chaincode is installed
    pod_exec "$pod" \
        peer chaincode list --installed \
        | egrep "\\b$CHAINCODE\\b" \
        | sed 's/^.*[Vv]ersion:[[:space:]]*\([^[:space:],]*\).*$/\1/' \
        | grep -Fx "$VERSION"

    if [[ $? -ne 0 ]]; then
        pod_exec "$pod" \
            peer chaincode install -n "$CHAINCODE" -v "$VERSION" -l golang "$CHAINCODEPATH"
        if [[ $? -ne 0 ]]; then
            echo "Unable to install chaincode" >&2
            exit 1
        fi
    fi
done

echo "$ORG peers have joined the channel: $CHANNEL" >&2
