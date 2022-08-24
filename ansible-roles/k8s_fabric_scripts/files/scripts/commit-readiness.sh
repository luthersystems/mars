#!/bin/bash

set -euxo pipefail

ORG=$1
CC_NAME=$2
CC_VERSION=$3
ENDORSEMENT_POLICY=$4
SEQ_NO=$5

NAMESPACE="fabric-$ORG"

source "${BASH_SOURCE%/*}/channel-utils.sh"

pod="$(select_first_cli_pod "$ORG" 0)"

# check whether this org sees all orgs as approving the chaincode
if ! chaincodeApprovals "$pod" | jq -er '.approvals | all'; then
    echo "${ORG} does not see chaincode approval from all orgs" >&2
    exit 1
fi

echo "${ORG} sees chaincode approval from all orgs" >&2
