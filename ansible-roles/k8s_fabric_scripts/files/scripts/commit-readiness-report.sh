#!/bin/bash

set -euxo pipefail

ORG=$1
CC_NAME=$2
CC_VERSION=$3
ENDORSEMENT_POLICY=$4
SEQ_NO=$5
NAMESPACE="fabric-$ORG"

source "${BASH_SOURCE%/*}/channel-utils.sh"

pod="$(select_first_pod "$ORG" 0)"

# check whether this org sees all orgs as approving the chaincode
chaincodeApprovals "$pod" | jq .
