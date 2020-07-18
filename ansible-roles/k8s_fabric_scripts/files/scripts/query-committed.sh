#!/bin/bash

set -euxo pipefail

ORG=$1
CC_NAME=$2
CC_VERSION=$3

NAMESPACE="fabric-$ORG"
source "${BASH_SOURCE%/*}/channel-utils.sh"

pod="$(select_first_pod "$ORG" 0)"

# look up the committed chaincode
if ! pod_exec "$pod" \
             peer lifecycle chaincode querycommitted \
             --channelID "$CHANNEL" \
             --name "$CC_NAME" \
             --output json |
        jq -e "select(.version = \"$CC_VERSION\")"
then
    echo {}
    exit 1
fi
