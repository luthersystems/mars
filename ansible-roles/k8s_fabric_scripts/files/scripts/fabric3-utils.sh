# shellcheck shell=bash
# Helpers for the Fabric 3 upgrade scripts (fabric3-preflight.sh,
# remove-system-channel.sh). Source channel-utils.sh first: these use its
# pod_exec, ORDERER_DOMAIN and ORDERER_TLS_ARGS.
#
# osnadmin runs in the orderer CLI pod, which has the orderer org's TLS
# certificate. The orderer admin endpoint admits any client certificate from
# the orderer TLS CA (fabric-orderer chart, channelParticipation), and the
# CLI pod reaches orderer<N> through the chart's orderer<N> ClusterIP
# Service, whose name is in the orderer TLS certificate.

ORDERER_ADMIN_PORT="${ORDERER_ADMIN_PORT:-7053}"
ORDERER_CLI_TLS_DIR=/etc/hyperledger/fabric/tls

# deployment_fabric_version <namespace> <deployment>: the image tag of the
# Fabric container (fabric-orderer or fabric-peer), empty when not found. A
# digest (@sha256:...) and a registry port (host:5000/...) are not the tag.
deployment_fabric_version() {
    kubectl -n "$1" get deployment "$2" -o json 2>/dev/null |
        jq -r '.spec.template.spec.containers[]
            | select(.name == "fabric-orderer" or .name == "fabric-peer")
            | .image | sub("@.*$"; "") | sub("^.*/"; "")
            | if test(":") then sub("^[^:]*:"; "") else "" end' | head -n 1
}

# fabric_minor <version>: "2.5" for "2.5.7" or "v2.5.7".
fabric_minor() {
    sed -E 's/^v//; s/^([0-9]+\.[0-9]+).*/\1/' <<<"$1"
}

# deployment_env <namespace> <deployment>: NAME=value lines of the Fabric
# container's env.
deployment_env() {
    kubectl -n "$1" get deployment "$2" -o json 2>/dev/null |
        jq -r '.spec.template.spec.containers[]
            | select(.name == "fabric-orderer" or .name == "fabric-peer")
            | .env[]? | "\(.name)=\(.value // "")"'
}

# osnadmin_args <orderer index>: the endpoint and mutual TLS flags.
osnadmin_args() {
    printf '%s\n' -o "orderer$1:$ORDERER_ADMIN_PORT" \
        --ca-file "$ORDERER_CLI_TLS_DIR/ca.crt" \
        --client-cert "$ORDERER_CLI_TLS_DIR/server.crt" \
        --client-key "$ORDERER_CLI_TLS_DIR/server.key"
}

# osnadmin_json <pod> <args...>: run osnadmin in the pod and print the JSON
# body. osnadmin prints "Status: <code>" then the body on stdout; any status
# other than 2xx fails. Only stdout is parsed: kubectl writes notes such as
# 'Defaulted container "fabric-cli" out of: ...' to stderr, and mixed into
# the body they made jq fail, so the pre-flight read no channels at all.
osnadmin_json() {
    local pod="$1" out err status rc=0
    shift
    err="$(mktemp)"
    out="$(pod_exec "$pod" osnadmin "$@" 2>"$err")" || rc=$?
    status="$(sed -n 's/^Status: \([0-9]*\).*/\1/p' <<<"$out" | head -n 1)"
    if ((rc != 0)) || [[ ! "$status" =~ ^2 ]]; then
        { echo "$out"; cat "$err"; } >&2
        rm -f "$err"
        return 1
    fi
    rm -f "$err"
    sed '/^Status: /d' <<<"$out"
}

# osnadmin_list <pod> <orderer index> [channel]: the channel list, or one
# channel's info.
osnadmin_list() {
    local pod="$1" i="$2" ch="${3-}" args
    mapfile -t args < <(osnadmin_args "$i")
    if [[ -n "$ch" ]]; then
        args+=(--channelID "$ch")
    fi
    osnadmin_json "$pod" channel list "${args[@]}"
}

# channel_config_json <pod> <channel> [orderer index]: the channel's current
# config (common.Config) as JSON, fetched through orderer<index> (default 0).
channel_config_json() {
    local pod="$1" ch="$2" i="${3:-0}" dir
    dir="/tmp/fabric3-$ch-$$"
    pod_exec "$pod" mkdir -p "$dir" >/dev/null &&
        pod_exec "$pod" peer channel fetch config "$dir/config.pb" -c "$ch" \
            -o "orderer$i.${ORDERER_DOMAIN}:7050" "${ORDERER_TLS_ARGS[@]}" >/dev/null 2>&1 &&
        pod_exec "$pod" configtxlator proto_decode --type common.Block --input "$dir/config.pb" |
        jq '.data.data[0].payload.data.config'
    local rc=$?
    pod_exec "$pod" rm -rf "$dir" >/dev/null 2>&1
    return "$rc"
}

# consensus_state: the ConsensusType state (STATE_NORMAL or
# STATE_MAINTENANCE) of the channel config JSON on stdin.
consensus_state() {
    jq -r '.channel_group.groups.Orderer.values.ConsensusType.value.state // "STATE_NORMAL"'
}

# orderer_height <pod> <orderer index>: the channel height on orderer<index>
# (newest block number + 1).
orderer_height() {
    local out n
    out="$(pod_exec "$1" peer channel fetch newest /dev/null -c "$CHANNEL" \
        -o "orderer$2.${ORDERER_DOMAIN}:7050" "${ORDERER_TLS_ARGS[@]}" 2>&1)" || return 1
    n="$(grep -Eo 'Received block: [0-9]+' <<<"$out" | tail -n 1 | awk '{print $3}')"
    [[ -n "$n" ]] || return 1
    echo "$((n + 1))"
}

# peer_height <peer pod>: the channel height on the peer.
peer_height() {
    local out
    out="$(pod_exec "$1" peer channel getinfo -c "$CHANNEL" 2>/dev/null)" || return 1
    grep -Eo '\{[^}]*\}' <<<"$out" | jq -e '.height'
}
