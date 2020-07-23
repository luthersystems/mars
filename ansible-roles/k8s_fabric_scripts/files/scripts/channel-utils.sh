CHANNEL=luther
ORDERER=orderer0.luther.systems:7050
ORDERER_CA=/etc/hyperledger/fabric/orderertls/tlsca.luther.systems-cert.pem

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
