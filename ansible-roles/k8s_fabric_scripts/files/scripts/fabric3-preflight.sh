#!/usr/bin/env bash
#
# Read-only check of a Fabric network before a step of the Fabric 3 upgrade
# (docs/fabric-3-upgrade.md, luthersystems/mars#341). It changes nothing.
#
# Fabric 3 removed the system channel, Kafka and Solo ordering and the legacy
# (lscc) chaincode lifecycle, and supports an upgrade only from 2.5. A network
# that still uses one of these does not fail at the upgrade: a 3.x orderer or
# peer starts, or stops at startup, and the network is down until someone
# rolls the images back. So each step checks the network state first.
#
# STEP (required) picks the checks that block:
#   remove-system-channel  before step 2: all orderers on 2.5 with channel
#                          participation on, osnadmin in the orderer CLI pod
#   upgrade                before step 3: no system channel on any orderer
# The other checks always run. A FAIL that blocks STEP exits 1; a WARN does
# not.
#
# Environment (the k8s_fabric_upgrade_v3 role sets these):
#   STEP, ORDERERC (orderer count), PEER_ORGS (space-separated org names,
#   for example "org1"), CHANNEL (default luther), FABRIC_DOMAIN,
#   FABRIC_ORDERER_DOMAIN, FABRIC_ORDERER_CLIENT_AUTH, ORDERER_NAMESPACE
#   (default fabric-orderer), ORDERER_ADMIN_PORT (default 7053),
#   CONFIG_FETCH_TRIES (default 6) and CONFIG_FETCH_WAIT (seconds, default
#   10): the tries of the channel config fetch
#
# Test: scripts/test-fabric3-upgrade.sh (a local 2.5.7 network and a fake kubectl).

set -uo pipefail

STEP="${STEP:?STEP must be remove-system-channel or upgrade}"
case "$STEP" in
remove-system-channel | upgrade) ;;
*)
    echo "STEP must be remove-system-channel or upgrade, not $STEP" >&2
    exit 2
    ;;
esac
ORDERERC="${ORDERERC:?}"
PEER_ORGS="${PEER_ORGS:?}"
ORDERER_NAMESPACE="${ORDERER_NAMESPACE:-fabric-orderer}"
ORDERER_ADMIN_PORT="${ORDERER_ADMIN_PORT:-7053}"
CONFIG_FETCH_TRIES="${CONFIG_FETCH_TRIES:-6}"
CONFIG_FETCH_WAIT="${CONFIG_FETCH_WAIT:-10}"

# channel-utils.sh needs NAMESPACE; the functions below set it per call.
NAMESPACE="$ORDERER_NAMESPACE"
# shellcheck source=channel-utils.sh
source "${BASH_SOURCE%/*}/channel-utils.sh"
# shellcheck source=fabric3-utils.sh
source "${BASH_SOURCE%/*}/fabric3-utils.sh"

blocking=0
rows=()

# result <PASS|WARN|FAIL|INFO> <blocks: any|remove-system-channel|upgrade|none> <check> <detail>
result() {
    local status="$1" blocks="$2" check="$3" detail="$4"
    if [[ "$status" == FAIL && ("$blocks" == any || "$blocks" == "$STEP") ]]; then
        blocking=1
    elif [[ "$status" == FAIL ]]; then
        # A FAIL for another step does not block this one.
        status="LATER"
    fi
    rows+=("$(printf '%-5s %-34s %s' "$status" "$check" "$detail")")
}

# Fabric versions of the orderer and peer images.
check_images() {
    local i tag min
    for i in $(seq 0 "$((ORDERERC - 1))"); do
        tag="$(deployment_fabric_version "$ORDERER_NAMESPACE" "fabric-orderer$i")"
        if [[ -z "$tag" ]]; then
            result FAIL any "orderer$i image" "no deployment fabric-orderer$i in $ORDERER_NAMESPACE"
            continue
        fi
        min="$(fabric_minor "$tag")"
        case "$min" in
        2.5) result PASS any "orderer$i image" "$tag" ;;
        3.*) result PASS any "orderer$i image" "$tag (already on 3)" ;;
        *) result FAIL any "orderer$i image" "$tag: Fabric 3 upgrades only from 2.5; upgrade to 2.5 first" ;;
        esac
    done
    local org pods pod
    for org in $PEER_ORGS; do
        pods="$(kubectl -n "fabric-$org" get deployments -l app.kubernetes.io/component=bcpeer -o name 2>/dev/null | sed 's!^deployment.apps/!!')"
        if [[ -z "$pods" ]]; then
            result FAIL any "$org peers" "no peer deployments in fabric-$org"
            continue
        fi
        for pod in $pods; do
            tag="$(deployment_fabric_version "fabric-$org" "$pod")"
            min="$(fabric_minor "$tag")"
            case "$min" in
            2.5 | 3.*) result PASS any "$pod image" "$tag" ;;
            *) result FAIL any "$pod image" "$tag: upgrade to 2.5 first" ;;
            esac
        done
    done
}

# fetch_channel_config <pod> <channel>: channel_config_json, tried
# CONFIG_FETCH_TRIES times, CONFIG_FETCH_WAIT seconds apart. Just after an
# orderer restart (step 1) or the system channel removal (step 2), the
# orderer can refuse a fetch until Raft has a leader for the channel again:
# a healthy network showed a FAIL here once on a first run after step 2.
# The last error goes to stderr.
fetch_channel_config() {
    local n
    for ((n = 1; ; n++)); do
        channel_config_json "$1" "$2" && return 0
        ((n >= CONFIG_FETCH_TRIES)) && return 1
        sleep "$CONFIG_FETCH_WAIT"
    done
}

# Channel config: consensus type, capabilities.
check_channel_config() {
    local org="${PEER_ORGS%% *}" pod cfg err_file err rc=0
    NAMESPACE="fabric-$org"
    if ! pod="$(select_first_cli_pod "$org" 0 2>/dev/null)"; then
        result FAIL any "$CHANNEL config" "no CLI pod for $org"
        return
    fi
    err_file="$(mktemp)"
    cfg="$(fetch_channel_config "$pod" "$CHANNEL" 2>"$err_file")" || rc=$?
    err="$(tail -n 1 "$err_file")"
    rm -f "$err_file"
    if ((rc != 0)); then
        result FAIL any "$CHANNEL config" \
            "could not fetch the channel config with $pod after $CONFIG_FETCH_TRIES tries: ${err:-no error text}"
        return
    fi
    local ctype state
    ctype="$(jq -r '.channel_group.groups.Orderer.values.ConsensusType.value.type' <<<"$cfg")"
    state="$(consensus_state <<<"$cfg")"
    case "$ctype" in
    etcdraft) result PASS any "$CHANNEL consensus" "etcdraft (Raft)" ;;
    BFT) result PASS any "$CHANNEL consensus" "BFT" ;;
    *) result FAIL any "$CHANNEL consensus" "$ctype: Fabric 3 has no Kafka or Solo; migrate to Raft first" ;;
    esac
    if [[ "$state" == STATE_NORMAL ]]; then
        result PASS any "$CHANNEL consensus state" "$state"
    else
        result FAIL any "$CHANNEL consensus state" "$state: the channel orders nothing in maintenance mode"
    fi
    local app ch
    app="$(jq -r '.channel_group.groups.Application.values.Capabilities.value.capabilities // {} | keys | join(",")' <<<"$cfg")"
    ch="$(jq -r '.channel_group.values.Capabilities.value.capabilities // {} | keys | join(",")' <<<"$cfg")"
    if grep -Eq '(^|,)V2_[05](,|$)' <<<"$app"; then
        result PASS any "$CHANNEL application capability" "$app"
    else
        result FAIL any "$CHANNEL application capability" "${app:-none}: needs V2_0 or V2_5 (the _lifecycle chaincode lifecycle)"
    fi
    if grep -Eq '(^|,)V(2_0|2_5|3_0)(,|$)' <<<"$ch"; then
        result PASS any "$CHANNEL channel capability" "$ch"
    else
        result FAIL any "$CHANNEL channel capability" "${ch:-none}: needs V2_0 or later"
    fi
}

# Chaincode: committed with _lifecycle, nothing instantiated with lscc.
check_chaincode() {
    local org="${PEER_ORGS%% *}" pod out
    NAMESPACE="fabric-$org"
    pod="$(select_first_cli_pod "$org" 0 2>/dev/null)" || return
    if out="$(pod_exec "$pod" peer lifecycle chaincode querycommitted -C "$CHANNEL" -O json 2>/dev/null)"; then
        local names
        names="$(jq -r '[.chaincode_definitions[]? | "\(.name) seq \(.sequence)"] | join(", ")' <<<"$out")"
        if [[ -n "$names" ]]; then
            result PASS any "_lifecycle chaincode" "$names"
        else
            result WARN none "_lifecycle chaincode" "none committed on $CHANNEL"
        fi
    else
        result FAIL any "_lifecycle chaincode" "querycommitted failed on $pod"
    fi
    # The legacy lifecycle is gone in 3.x. A 2.5 peer still answers this.
    if out="$(pod_exec "$pod" peer chaincode list --instantiated -C "$CHANNEL" 2>/dev/null)"; then
        if grep -q 'Name:' <<<"$out"; then
            result FAIL any "legacy (lscc) chaincode" "instantiated with the legacy lifecycle; redeploy with _lifecycle first"
        else
            result PASS any "legacy (lscc) chaincode" "none"
        fi
    else
        result INFO none "legacy (lscc) chaincode" "not checked (the peer does not answer lscc, as on 3.x)"
    fi
}

# System channel and channel participation on each orderer.
check_system_channel() {
    local i env bootstrap list sys pod present=0 unknown=0
    NAMESPACE="$ORDERER_NAMESPACE"
    pod="$(select_first_cli_pod orderer 0 2>/dev/null)" || pod=""
    if [[ -z "$pod" ]]; then
        result FAIL remove-system-channel "orderer CLI pod" "none in $ORDERER_NAMESPACE (set k8s_fabric_orderer_cli)"
    elif pod_exec "$pod" which osnadmin >/dev/null 2>&1; then
        result PASS any "osnadmin in $pod" "present"
    else
        result FAIL remove-system-channel "osnadmin in $pod" "missing; the CLI image needs Fabric 2.3 or later tools"
    fi
    for i in $(seq 0 "$((ORDERERC - 1))"); do
        env="$(deployment_env "$ORDERER_NAMESPACE" "fabric-orderer$i")"
        bootstrap="$(awk -F= '$1 == "ORDERER_GENERAL_BOOTSTRAPMETHOD" {print $2}' <<<"$env")"
        if [[ "$bootstrap" != none ]] || ! grep -q '^ORDERER_CHANNELPARTICIPATION_ENABLED=true$' <<<"$env"; then
            result FAIL remove-system-channel "orderer$i channel participation" "off: run step 1 (k8s_fabric_orderer_channel_participation)"
            result FAIL upgrade "orderer$i system channel" "unknown: channel participation is off"
            unknown=1
            continue
        fi
        result PASS any "orderer$i channel participation" "on"
        if [[ -z "$pod" ]] || ! list="$(osnadmin_list "$pod" "$i")"; then
            result FAIL any "orderer$i system channel" "osnadmin channel list failed on orderer$i:$ORDERER_ADMIN_PORT"
            unknown=1
            continue
        fi
        sys="$(jq -r '.systemChannel.name // empty' <<<"$list")"
        if [[ -n "$sys" ]]; then
            result FAIL upgrade "orderer$i system channel" "$sys: run step 2 (remove-system-channel.sh)"
            present=1
        else
            result PASS any "orderer$i system channel" "none"
        fi
        if jq -e --arg ch "$CHANNEL" '.channels[]? | select(.name == $ch)' <<<"$list" >/dev/null; then
            result PASS any "orderer$i serves $CHANNEL" "yes"
        else
            result FAIL any "orderer$i serves $CHANNEL" "no: the orderer does not list $CHANNEL"
        fi
    done
    if ((present == 0 && unknown == 0)); then
        result INFO none "system channel" "removed on every orderer"
    fi
}

# Ledger heights: every peer at the orderers' height.
check_heights() {
    local org="${PEER_ORGS%% *}" pod i h peak=0 hp
    NAMESPACE="fabric-$org"
    pod="$(select_first_cli_pod "$org" 0 2>/dev/null)" || return
    for i in $(seq 0 "$((ORDERERC - 1))"); do
        if h="$(orderer_height "$pod" "$i")"; then
            ((h > peak)) && peak="$h"
            result PASS any "orderer$i $CHANNEL height" "$h"
        else
            result WARN none "orderer$i $CHANNEL height" "could not read; is orderer$i up?"
        fi
    done
    for org in $PEER_ORGS; do
        NAMESPACE="fabric-$org"
        for pod in $(select_peer_pods "$org" 2>/dev/null); do
            if ! hp="$(peer_height "$pod")"; then
                result WARN none "$pod height" "could not read"
            elif ((peak == 0)); then
                # No orderer height to compare with.
                result WARN none "$pod height" "$hp, not compared: no orderer height"
            elif ((hp >= peak)); then
                result PASS any "$pod height" "$hp"
            else
                result WARN none "$pod height" "$hp, orderers at $peak: let it catch up first"
            fi
        done
    done
}

# Chaincode mode: Docker chaincode builds with the upstream 3.x images.
check_chaincode_mode() {
    local org docker
    for org in $PEER_ORGS; do
        # The fabric-peer chart runs Docker chaincode in a dind container
        # named docker-chaincodes.
        docker="$(kubectl -n "fabric-$org" get deployments -l app.kubernetes.io/component=bcpeer -o json 2>/dev/null |
            jq -r '[.items[].spec.template.spec | (.containers // []) + (.initContainers // []) | .[].name] | any(. == "docker-chaincodes")')"
        if [[ "$docker" == true ]]; then
            result WARN none "$org chaincode" "Docker (dind): 3.x builds with hyperledger/fabric-ccenv:3.1, not the hardened image; test a chaincode build on test first"
        else
            result PASS any "$org chaincode" "external or chaincode as a service"
        fi
    done
}

check_images
check_channel_config
check_chaincode
check_system_channel
check_heights
check_chaincode_mode

echo
echo "Fabric 3 pre-flight, step $STEP, channel $CHANNEL"
printf '%-5s %-34s %s\n' STATE CHECK DETAIL
printf '%s\n' "${rows[@]}"
echo
echo "LATER: blocks a later step, not this one. Take a backup before step 2 or 3"
echo "(snapshot archiver or volume snapshots): there is no way back from step 2."
if ((blocking)); then
    echo "NOT READY for step $STEP"
    exit 1
fi
echo "READY for step $STEP"
