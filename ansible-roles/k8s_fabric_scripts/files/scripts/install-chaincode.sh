#!/bin/bash

set -euxo pipefail

ORG=$1
CC_NAME=$2
CC_VERSION=$3
NAMESPACE="fabric-$ORG"

source "${BASH_SOURCE%/*}/channel-utils.sh"

CC_SRC_DIR=/opt/gopath/src/github.com/hyperledger/fabric/examples/chaincode/go
CC_SRC_PATH="${CC_SRC_DIR}/${CC_NAME}-${CC_VERSION}.tar.gz"

pods="$(select_pods "$ORG")"

installedOnPod() {
    POD=$1

    chaincodePackageID "$POD" >/dev/null
}

changed=0
for pod in $pods; do
    if ! installedOnPod "$pod"; then
        if ! pod_exec "$pod" peer lifecycle chaincode install "$CC_SRC_PATH"; then
            echo "Unable to install chaincode" >&2
            exit 1
        fi
        changed=1
    fi
done

if [[ "$changed" -eq 0 ]]; then
    echo "${ORG} peers have already installed the chaincode" >&2
else
    echo "${ORG} peers have installed the chaincode" >&2
fi
