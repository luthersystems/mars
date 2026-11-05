#!/usr/bin/env bash
#
# Step 2 of the Fabric 3 upgrade (docs/fabric-3-upgrade.md,
# luthersystems/mars#341): remove the orderer system channel.
#
# Why: Fabric 3 has no system channel. A 3.x orderer whose ledger still has
# one stops at startup ("found system channel config block in the ledger").
# Our networks used the system channel only to boot the orderers and to create
# the application channel once, so nothing at run time needs it.
#
# Before this script, step 1 rolls the orderers (still on 2.5) with channel
# participation on, so each orderer has the osnadmin endpoint and boots with
# BootstrapMethod "none". It then still loads the system channel from its
# ledger.
#
# What it does, as the Fabric 2.5 procedure ("Remove the system channel" in
# create_channel_participation) says:
#   1. Puts the system channel into maintenance mode, so no one can create a
#      channel through it while it goes away. The orderer CLI pod signs the
#      config update as the orderer org admin.
#   2. On each orderer in turn: osnadmin channel remove. The orderer stops
#      and restarts its other channels without the system channel, which
#      takes a second or two. Then it waits until the orderer serves CHANNEL
#      again, active, as a consenter, at least at its earlier height, before
#      it goes to the next orderer. One at a time keeps Raft quorum on a
#      network with three or more orderers; a one-orderer network pauses
#      ordering for those seconds.
#   3. Checks that no orderer lists a system channel.
#
# It is safe to run again: it skips each part that is already done. There is
# no way back after part 2 except a restore of the orderer volumes, so take a
# backup first.
#
# DRY_RUN=true prints what it would do and changes nothing.
#
# Environment (the k8s_fabric_upgrade_v3 role sets these): ORDERERC,
# CHANNEL (default luther), FABRIC_DOMAIN, FABRIC_ORDERER_DOMAIN,
# FABRIC_ORDERER_CLIENT_AUTH, ORDERER_NAMESPACE (default fabric-orderer),
# ORDERER_ADMIN_PORT (default 7053), WAIT_SECONDS (default 300).
#
# Test: scripts/test-fabric3-upgrade.sh runs it against a local three-orderer
# 2.5.7 network through a fake kubectl, then upgrades the network to 3.1.5.

set -euo pipefail

ORDERERC="${ORDERERC:?}"
ORDERER_NAMESPACE="${ORDERER_NAMESPACE:-fabric-orderer}"
DRY_RUN="${DRY_RUN:-false}"
WAIT_SECONDS="${WAIT_SECONDS:-300}"

NAMESPACE="$ORDERER_NAMESPACE"
# shellcheck source=channel-utils.sh
source "${BASH_SOURCE%/*}/channel-utils.sh"
# shellcheck source=fabric3-utils.sh
source "${BASH_SOURCE%/*}/fabric3-utils.sh"

log() { echo "remove-system-channel: $*" >&2; }

pod="$(select_first_cli_pod orderer 0)"

# The system channel name, from the first orderer that still lists one.
sys=""
for i in $(seq 0 "$((ORDERERC - 1))"); do
    if ! list="$(osnadmin_list "$pod" "$i")"; then
        log "osnadmin channel list failed on orderer$i. Is channel participation on (step 1)?"
        exit 1
    fi
    name="$(jq -r '.systemChannel.name // empty' <<<"$list")"
    if [[ -n "$name" ]]; then
        sys="$name"
        break
    fi
done
if [[ -z "$sys" ]]; then
    log "no orderer lists a system channel: nothing to do"
    exit 0
fi
if [[ "$sys" == "$CHANNEL" ]]; then
    log "the system channel is $CHANNEL, the application channel: refusing"
    exit 1
fi
log "system channel: $sys"

# 1. Maintenance mode. A removal on every orderer makes this moot, but an
# earlier run may have stopped part way, so it runs while any orderer still
# has the channel. channel_config_json fetches through orderer<i>; use one
# that still has the system channel.
#
# Raft can drop an update that an orderer accepted ("Successfully
# submitted") and forwarded to a leader that then loses its leadership, which
# is likely just after step 1 restarted the orderers. So when the state does
# not change in 30 seconds, it fetches the config again and sends the update
# again. A second update on a config that already changed is rejected, so a
# resend cannot apply the change twice.
sys_state() { # sys_state <orderer index>: the system channel's consensus state
    channel_config_json "$pod" "$sys" "$1" | consensus_state
}

send_maintenance() { # send_maintenance <orderer index> <config JSON>
    local i="$1" cfg="$2" dir="/tmp/fabric3-maint-$$"
    pod_exec "$pod" mkdir -p "$dir"
    printf '%s\n' "$cfg" | kubectl -n "$NAMESPACE" exec -i "$pod" -- sh -c "cat > $dir/config.json"
    pod_exec "$pod" sh -c "jq '.channel_group.groups.Orderer.values.ConsensusType.value.state = \"STATE_MAINTENANCE\"' $dir/config.json > $dir/modified.json"
    pod_exec "$pod" configtxlator proto_encode --type common.Config --input "$dir/config.json" --output "$dir/config.pb"
    pod_exec "$pod" configtxlator proto_encode --type common.Config --input "$dir/modified.json" --output "$dir/modified.pb"
    pod_exec "$pod" configtxlator compute_update --channel_id "$sys" --original "$dir/config.pb" --updated "$dir/modified.pb" --output "$dir/update.pb"
    pod_exec "$pod" sh -c "configtxlator proto_decode --type common.ConfigUpdate --input $dir/update.pb |
        jq --arg ch '$sys' '{payload: {header: {channel_header: {channel_id: \$ch, type: 2}}, data: {config_update: .}}}' > $dir/envelope.json"
    pod_exec "$pod" configtxlator proto_encode --type common.Envelope --input "$dir/envelope.json" --output "$dir/envelope.pb"
    # A failed send (no Raft leader yet) is retried by the caller.
    pod_exec "$pod" peer channel update -f "$dir/envelope.pb" -c "$sys" \
        -o "orderer$i.${ORDERER_DOMAIN}:7050" "${ORDERER_TLS_ARGS[@]}" >&2 || true
    pod_exec "$pod" rm -rf "$dir"
}

# maintenance <orderer index>: until the system channel is in maintenance
# mode, through orderer<index>.
maintenance() {
    local i="$1" cfg state deadline=$((SECONDS + WAIT_SECONDS)) resend sent=false
    while :; do
        if cfg="$(channel_config_json "$pod" "$sys" "$i")"; then
            state="$(consensus_state <<<"$cfg")"
            if [[ "$state" == STATE_MAINTENANCE ]]; then
                log "$sys is in maintenance mode"
                return
            fi
            if [[ "$DRY_RUN" == true ]]; then
                log "DRY_RUN: would put $sys into maintenance mode (now $state)"
                return
            fi
            if [[ "$sent" == true ]]; then
                log "$sys is not in maintenance mode 30s after the update: sending it again"
            fi
            log "putting $sys into maintenance mode"
            send_maintenance "$i" "$cfg"
            sent=true
            resend=$((SECONDS + 30))
            while ((SECONDS < resend)) && [[ "$(sys_state "$i" 2>/dev/null)" != STATE_MAINTENANCE ]]; do
                sleep 3
            done
        else
            log "cannot fetch the $sys config through orderer$i; trying again"
            sleep 3
        fi
        if ((SECONDS > deadline)); then
            log "$sys did not enter maintenance mode in ${WAIT_SECONDS}s"
            exit 1
        fi
    done
}

# wait_served <orderer index> <min height>: until orderer<i> serves CHANNEL
# as an active consenter at min height or more.
wait_served() {
    local i="$1" min="$2" info deadline=$((SECONDS + WAIT_SECONDS))
    while :; do
        if info="$(osnadmin_list "$pod" "$i" "$CHANNEL" 2>/dev/null)" &&
            jq -e --argjson min "$min" \
                '.status == "active" and .consensusRelation == "consenter" and .height >= $min' \
                <<<"$info" >/dev/null; then
            log "orderer$i serves $CHANNEL: $(jq -c '{status, consensusRelation, height}' <<<"$info")"
            return
        fi
        if ((SECONDS > deadline)); then
            log "orderer$i does not serve $CHANNEL after ${WAIT_SECONDS}s: $info"
            log "stopping before the next orderer to keep quorum; look at the orderer$i logs"
            exit 1
        fi
        sleep 3
    done
}

done_maint=false
for i in $(seq 0 "$((ORDERERC - 1))"); do
    list="$(osnadmin_list "$pod" "$i")"
    if [[ -z "$(jq -r '.systemChannel.name // empty' <<<"$list")" ]]; then
        log "orderer$i: system channel already removed"
        continue
    fi
    if [[ "$done_maint" == false ]]; then
        maintenance "$i"
        done_maint=true
    fi
    before="$(osnadmin_list "$pod" "$i" "$CHANNEL" | jq -r '.height')"
    if [[ "$DRY_RUN" == true ]]; then
        log "DRY_RUN: would remove $sys from orderer$i ($CHANNEL height $before)"
        continue
    fi
    log "orderer$i: removing $sys ($CHANNEL height $before)"
    mapfile -t args < <(osnadmin_args "$i")
    osnadmin_json "$pod" channel remove "${args[@]}" --channelID "$sys" >/dev/null
    wait_served "$i" "$before"
done

[[ "$DRY_RUN" == true ]] && exit 0

for i in $(seq 0 "$((ORDERERC - 1))"); do
    if [[ -n "$(osnadmin_list "$pod" "$i" | jq -r '.systemChannel.name // empty')" ]]; then
        log "orderer$i still lists a system channel"
        exit 1
    fi
done
log "done: no orderer has a system channel"
