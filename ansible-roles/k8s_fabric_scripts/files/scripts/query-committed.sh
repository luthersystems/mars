#!/bin/bash

set -euxo pipefail

ORG=$1
CC_NAME=$2
CC_VERSION=$3

NAMESPACE="fabric-$ORG"
source "${BASH_SOURCE%/*}/channel-utils.sh"

query() {
    pod="$(select_first_pod "$ORG" 0)"

    # look up the committed chaincode
    pod_exec "$pod" \
             peer lifecycle chaincode querycommitted \
             --channelID "$CHANNEL" \
             --name "$CC_NAME" \
             --output json
}

currentVersion() {
    queryResult=$1

    echo "$queryResult" | jq -e "select(.version == \"$CC_VERSION\")"
}

# unable to query chaincode, assume it has not been committed yet
if ! committed="$(query)"; then
    echo {}
    exit 1
fi

# return the current commit data on failure
if !(currentVersion "$committed"); then
    set +x
    echo "$committed"
    exit 1
fi
