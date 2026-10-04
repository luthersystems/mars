# The Fabric roles set FABRIC_DOMAIN from their *_domain_root var. The default
# only suits luther.systems networks and scripts that never reach the orderer.
FABRIC_DOMAIN="${FABRIC_DOMAIN:-luther.systems}"
# Set FABRIC_ORDERER_DOMAIN when the orderer org's domain differs from
# FABRIC_DOMAIN.
ORDERER_DOMAIN="${FABRIC_ORDERER_DOMAIN:-$FABRIC_DOMAIN}"

if [ -z "$NAMESPACE" ]; then
    echo "No NAMESPACE" >&2
    exit 1
fi

if [ -z "$FABRIC_DOMAIN" ]; then
    echo "No FABRIC_DOMAIN" >&2
    exit 1
fi


# CHANNEL may be overridden if it is already defined
CHANNEL="${CHANNEL:-luther}"
ORDERER="orderer0.${ORDERER_DOMAIN}:7050"
ORDERER_CA="/etc/hyperledger/fabric/orderertls/tlsca.${ORDERER_DOMAIN}-cert.pem"
COLLECTIONS_PATH=/etc/hyperledger/fabric/collections-config/collections.json
CORE_PEER_TLS_CERT_FILE="/etc/hyperledger/fabric/tls/server.crt"
CORE_PEER_TLS_KEY_FILE="/etc/hyperledger/fabric/tls/server.key"

# Set FABRIC_ORDERER_CLIENT_AUTH=true when the orderer requires client TLS
# (General.TLS.ClientAuthRequired). Orderer calls then present the peer's TLS
# cert and key.
ORDERER_CLIENT_AUTH_ARGS=()
if [[ "${FABRIC_ORDERER_CLIENT_AUTH:-false}" == "true" ]]; then
    ORDERER_CLIENT_AUTH_ARGS=(--clientauth --certfile "$CORE_PEER_TLS_CERT_FILE" --keyfile "$CORE_PEER_TLS_KEY_FILE")
fi
# The ${a[@]+...} form keeps an empty array safe under set -u on older bash.
ORDERER_TLS_ARGS=(--tls --cafile "$ORDERER_CA" ${ORDERER_CLIENT_AUTH_ARGS[@]+"${ORDERER_CLIENT_AUTH_ARGS[@]}"})

select_first_cli_pod() {
    select_cli_pods "$@" | head -n 1
}

select_cli_pods() {
    org="$1"
    index="${2-}"
    component='bccli'
    select_pods "$org" "$component" "$index"
}

select_peer_pods() {
    org="$1"
    index="${2-}"
    component='bcpeer'
    select_pods "$org" "$component" "$index"
}

select_pods() {
    org="$1"
    component="$2"
    index="${3-}"

    pod_selector="app.kubernetes.io/component=${component},fabric/organization=${org}"
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


pod_copy() {
    pod="$1"
    shift

    kubectl cp "$1" "$NAMESPACE"/"$pod":"$2"
}


pod_fetch() {
    pod="$1"
    shift

    kubectl cp "$NAMESPACE"/"$pod":"$1" "$2"
}

pod_exec() {
    pod="$1"
    shift

    kubectl -n "$NAMESPACE" exec "$pod" -- "$@"
}

pod_container_exec() {
    pod="$1"
    container="$2"
    shift 2

    kubectl -n "$NAMESPACE" exec "$pod" -c "$container" -- "$@"
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
  | select((.label == "${CC_NAME}_${CC_VERSION}") or (.label == "${CC_NAME}-${CC_VERSION}"))
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
