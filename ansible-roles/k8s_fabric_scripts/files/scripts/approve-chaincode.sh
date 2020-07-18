#!/bin/bash

set -euxo pipefail

ORG=$1
MSP=$2
CC_NAME=$3
CC_VERSION=$4
ENDORSEMENT_POLICY=$5
SEQ_NO=$6
NAMESPACE="fabric-$ORG"

source "${BASH_SOURCE%/*}/channel-utils.sh"

pod="$(select_first_pod "$ORG" 0)"

approvedByOrg() {
    POD=$1

    # check whether this org has approved the chaincode
    chaincodeApprovals "$POD" |
        jq -er ".approvals.${MSP} == true"
}

if ! package_id="$(chaincodePackageID "$pod" "$CC_NAME" "$CC_VERSION")"; then
    echo "chaincode not installed on peer" >&2
    exit 1
fi

changed=0
if ! approvedByOrg "$pod"; then
    if ! pod_exec "$pod" peer lifecycle chaincode approveformyorg \
             --channelID "$CHANNEL" --tls \
             --cafile "$ORDERER_CA" --orderer "$ORDERER" \
             --name "$CC_NAME" --version "$CC_VERSION" \
             --collections-config "$COLLECTIONS_PATH" \
             --signature-policy "$ENDORSEMENT_POLICY" \
             --sequence "$SEQ_NO" \
             --package-id "$package_id"
    then
        echo "Failed to approve chaincode" >&2
        exit 1
    fi
    changed=1
fi

if [[ "$changed" -eq 0 ]]; then
    echo "${ORG} has already approved the chaincode" >&2
else
    echo "${ORG} has approved the chaincode" >&2
fi
