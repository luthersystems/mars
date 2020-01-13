#!/bin/bash

set -x

ORG=$1
CHAINCODE=$2
VERSION=$3
NAMESPACE="fabric-$ORG"
CHANNEL=luther

CHAINCODEPATH=/opt/gopath/src/github.com/hyperledger/fabric/examples/chaincode/go/$CHAINCODE-$VERSION.cds

POD_SELECTOR=app.kubernetes.io/component=bccli

PODS=$(kubectl -n "$NAMESPACE" get pods -l "$POD_SELECTOR" -o name | sed 's!^pod/!!')

if [[ -z "$PODS" ]]; then
    echo "No pods found in namespace $NAMESPACE: $POD_SELECTOR"
    exit 1
fi

for POD in $PODS; do
    # Determine if the exact version of the specified chaincode is installed
    kubectl -n "$NAMESPACE" exec "$POD" -- \
        peer chaincode list --installed \
        | egrep "\\b$CHAINCODE\\b" \
        | sed 's/^.*[Vv]ersion:[[:space:]]*\([^[:space:],]*\).*$/\1/' \
        | grep -Fx "$VERSION"

    if [[ $? -ne 0 ]]; then
        kubectl -n "$NAMESPACE" exec "$POD" -- \
            peer chaincode install -n "$CHAINCODE" -v "$VERSION" -l golang "$CHAINCODEPATH"
        if [[ $? -ne 0 ]]; then
            echo "Unable to install chaincode" >&2
            exit 1
        fi
    fi
done

echo "$ORG peers have joined the channel: $CHANNEL" >&2
