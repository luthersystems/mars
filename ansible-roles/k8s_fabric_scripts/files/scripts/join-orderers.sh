#!/usr/bin/env bash
#
# Joins each orderer to CHANNEL with osnadmin, from the channel's genesis
# block (luthersystems/mars#343).
#
# Why: a network with channel participation on has no orderer system
# channel, so `peer channel create` (create-channel.sh without it) has
# nothing to send the channel creation to. Fabric 3 has no system channel at
# all. The orderers instead join the channel from its genesis block, which
# fabric-network-builder generate writes as channel-artifacts/<CHANNEL>.block
# (configtxgen -outputBlock with an application channel profile that has the
# orderer group and the anchor peers). The peers then join with the block
# the orderers serve, as before.
#
# osnadmin runs in the orderer CLI pod (orderer org TLS certificate) against
# each orderer's admin Service orderer<N> (fabric-orderer chart,
# channelParticipation). An orderer that already serves CHANNEL is skipped,
# so the script is safe to run again, and it does nothing on a network whose
# orderers all joined the channel before.
#
# Environment (the k8s_fabric_channel role sets these): ORDERERC, CHANNEL
# (default luther), FABRIC_DOMAIN, FABRIC_ORDERER_DOMAIN, ORDERER_NAMESPACE
# (default fabric-orderer), ORDERER_ADMIN_PORT (default 7053).
#
# Test: scripts/test-fabric3-upgrade.sh, "new network" section.

set -euo pipefail

ORDERERC="${ORDERERC:?}"
ORDERER_NAMESPACE="${ORDERER_NAMESPACE:-fabric-orderer}"

NAMESPACE="$ORDERER_NAMESPACE"
# shellcheck source=channel-utils.sh
source "${BASH_SOURCE%/*}/channel-utils.sh"
# shellcheck source=fabric3-utils.sh
source "${BASH_SOURCE%/*}/fabric3-utils.sh"

# The path in the CLI pod, whose working directory has the channel artifacts.
GENESISBLOCK="${GENESISBLOCK:-./channel-artifacts/${CHANNEL}.block}"

log() { echo "join-orderers: $*" >&2; }

pod="$(select_first_cli_pod orderer 0)"

for i in $(seq 0 "$((ORDERERC - 1))"); do
    mapfile -t args < <(osnadmin_args "$i")
    status="$(osnadmin_status "$pod" channel list --channelID "$CHANNEL" "${args[@]}")" || {
        log "osnadmin cannot reach orderer$i. Is channel participation on?"
        exit 1
    }
    case "$status" in
    2*)
        log "orderer$i already serves $CHANNEL"
        continue
        ;;
    404) ;;
    *)
        log "osnadmin channel list on orderer$i: status $status"
        exit 1
        ;;
    esac
    if ! pod_exec "$pod" test -f "$GENESISBLOCK" 2>/dev/null; then
        log "the orderer CLI pod has no $GENESISBLOCK. Generate the channel" \
            "artifacts with fabric-network-builder v0.1.0 or later, which" \
            "writes the channel genesis block, and roll the CLI pods."
        exit 1
    fi
    if ! osnadmin_json "$pod" channel join --channelID "$CHANNEL" \
        --config-block "$GENESISBLOCK" "${args[@]}" >/dev/null; then
        log "osnadmin channel join failed on orderer$i. An orderer that still has" \
            "a system channel cannot join a channel this way (docs/fabric-3-upgrade.md)."
        exit 1
    fi
    log "orderer$i joined $CHANNEL"
done
