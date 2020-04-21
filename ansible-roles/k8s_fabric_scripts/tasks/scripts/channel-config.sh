CHANNEL=luther
ORDERER=orderer0.luther.systems:7050
ORDERER_CA=/etc/hyperledger/fabric/orderertls/tlsca.luther.systems-cert.pem

if [ -z "$NAMESPACE" ]; then
    echo "No NAMESPACE" >&2
    exit 1
fi

pod_select() {
    POD_SELECTOR="app.kubernetes.io/component=bccli,fabric/organization=$1,fabric/organization-index=$2"
    POD="$(kubectl -n "$NAMESPACE" get pods -l "$POD_SELECTOR" -o name | head -n 1 | sed 's!^pod/!!')"
    if [ -z "$POD" ]; then
        echo "No POD matching selector: $POD_SELECTOR" >&2
        exit 1
    fi
}

pod_exec() {
    kubectl -n "$NAMESPACE" exec "$POD" -- "$@"
}
