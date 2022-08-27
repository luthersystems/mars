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

if ! package_id="$(chaincodePackageID "$pod" "$CC_NAME" "$CC_VERSION")"; then
    echo "chaincode not installed on peer" >&2
    exit 1
fi

ORDERER_TLSCA_CERT="$(pod_exec "$pod" cat "$ORDERER_CA")"

if [ -z "$ORDERER_TLSCA_CERT" ]; then
  echo "orderer tlsca cert not found" >&2
  exit 1
fi

COLLECTIONS_CONFIG="$(pod_exec "$pod" cat "$COLLECTIONS_PATH")"

if [ -z "$COLLECTIONS_CONFIG" ]; then
  echo "collections config not found" >&2
  exit 1
fi


cat << EOSCRIPT
#!/bin/bash

cat > orderer-tlsca.pem <<EOF
$ORDERER_TLSCA_CERT
EOF

cat > collections.json <<EOF
$COLLECTIONS_CONFIG
EOF

# TODO: --clientauth --certfile "$CORE_PEER_TLS_CERT_FILE" --keyfile "$CORE_PEER_TLS_KEY_FILE"
peer lifecycle chaincode approveformyorg \\
  --tls --cafile orderer-tlsca.pem --orderer "$ORDERER" \\
  --channelID "$CHANNEL" \\
  --name "$CC_NAME" --version "$CC_VERSION" \\
  --collections-config collections.json \
  --signature-policy "$ENDORSEMENT_POLICY" \\
  --sequence "$SEQ_NO" \\
  --package-id \$PACKAGE_ID
EOSCRIPT
