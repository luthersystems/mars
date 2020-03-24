export KUBECONFIG=/opt/k8s/kubeconfig.yaml

KUBECTL_NAMESPACE=default

setkns() {
    if [ -z "$1" ]; then
        echo 'no namespace provided' 1>&2
        return 1
    fi
    KUBECTL_NAMESPACE="$1"
}

kns() {
    if [[ $# -eq 0 ]]; then
        echo KUBECTL_NAMESPACE="$KUBECTL_NAMESPACE"
        return 0
    fi
    kubectl -n "$KUBECTL_NAMESPACE" $@
}
