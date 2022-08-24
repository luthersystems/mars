#!/bin/bash

set -euxo pipefail

ORG=$1
PEER_IDX=$2
CC_NAME=$3
CC_VERSION=$4
CC_LABEL=$5
TIMESTAMP=$6
EXTERNALCC=$7

NAMESPACE="fabric-$ORG"

source "${BASH_SOURCE%/*}/channel-utils.sh"

pod="$(select_first_cli_pod "$ORG" "$PEER_IDX")"

if ccid="$(chaincodePackageID "$pod" "$CC_NAME" "$CC_VERSION")"; then
    echo "$ccid"
    echo "${ORG} peer ${PEER_IDX} has already installed the chaincode" >&2
    exit 0
fi

cat >/tmp/connection.json <<EOF
  {
    "dial_timeout": "10s",
    "tls_required": false,
    "client_auth_required": false
  }
EOF
mkdir -p /tmp/metadata
echo $CC_LABEL > /tmp/metadata/cc_label
GZIP=-n tar -zcf /tmp/code.tar.gz -C /tmp --mtime=$TIMESTAMP connection.json metadata
cat >/tmp/metadata.json <<EOF
  {
    "path": "main",
    "type": "external",
    "label": "${CC_NAME}-${CC_VERSION}"
  }
EOF

CC_SRC_DIR=/opt/gopath/src/github.com/hyperledger/fabric/examples/chaincode/go
CC_SRC_PATH="${CC_SRC_DIR}/${CC_NAME}-${CC_VERSION}.tar.gz"

if [ "$EXTERNALCC" != "True" ]; then
  cp $CC_SRC_PATH /tmp/chaincode.tar.gz
else
  GZIP=-n tar -zcf /tmp/chaincode.tar.gz -C /tmp --mtime=$TIMESTAMP metadata.json code.tar.gz
fi

if ! pod_copy "$pod" /tmp/chaincode.tar.gz /tmp/chaincode.tar.gz; then
    echo "Unable to copy chaincode" >&2
    exit 1
fi

if ! pod_exec "$pod" peer lifecycle chaincode install /tmp/chaincode.tar.gz; then
    echo "Unable to install chaincode" >&2
    exit 1
fi

if ! chaincodePackageID "$pod" "$CC_NAME" "$CC_VERSION"; then
    echo "Unable to find chaincode on peer" >&2
    exit 1
fi

echo "${ORG} peer ${PEER_IDX} has installed the chaincode" >&2
