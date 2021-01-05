# CHANNEL may be overridden if it is already defined
CHANNEL="${CHANNEL:-luther}"
ORDERER=orderer0.luther.systems:7050
ORDERER_CA=/etc/hyperledger/fabric/orderertls/tlsca.luther.systems-cert.pem
COLLECTIONS_PATH=/etc/hyperledger/fabric/collections-config/collections.json

if [ -z "$NAMESPACE" ]; then
    echo "No NAMESPACE" >&2
    exit 1
fi

select_first_pod() {
    select_pods "$@" | head -n 1
}

select_pods() {
    org="$1"
    index="${2-}"

    pod_selector="app.kubernetes.io/component=bccli,fabric/organization=${org}"
    if [[ -n "$index" ]]; then
        pod_selector="${pod_selector},fabric/organization-index=${index}"
    fi
    pods="$(kubectl -n "$NAMESPACE" get pods -l "$pod_selector" -o name | sed 's!^pod/!!')"
    if [[ -z "$pods" ]]; then
        echo "No pods matching selector: $pod_selector" >&2
        exit 1
    fi
    echo "$pods"
}

pod_exec() {
    pod="$1"
    shift

    kubectl -n "$NAMESPACE" exec "$pod" -- "$@"
}

chaincodePackageID() {
    POD=$1

    if ! pod_exec "$POD" peer lifecycle chaincode queryinstalled -O json >chaincodes.json; then
        echo "Error querying installed chaincode" >&2
        exit 1
    fi

    # check whether any chaincodes are installed
    if jq -er '. == {}' chaincodes.json >&2; then
        echo "no chaincodes installed on peer" >&2
        return 1
    fi

    # check for expected chaincode name/version, printing package_id
    query=$(cat <<EOF
.installed_chaincodes[]
  | select(.label == "${CC_NAME}_${CC_VERSION}")
  | .package_id
EOF
    )
    if ! jq -er "$query" chaincodes.json; then
        echo "chaincode not installed on peer" >&2
        return 1
    fi

    echo "chaincode already installed on peer" >&2
}

chaincodeApprovals() {
    POD=$1

    if ! pod_exec "$POD" peer lifecycle chaincode checkcommitreadiness \
             --channelID "$CHANNEL" \
             --name "$CC_NAME" --version "$CC_VERSION" \
             --collections-config "$COLLECTIONS_PATH" \
             --signature-policy "$ENDORSEMENT_POLICY" \
             --sequence "$SEQ_NO" \
             --output json
    then
        echo "Error querying chaincode approvals" >&2
        exit 1
    fi
}
