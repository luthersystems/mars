#!/bin/bash

set -xeuo pipefail

ORG_MSP=$1
ORG_JSON_PATH=$2
UPDATE_PB_PATH=$3
ORG_JSON_NAME=$(basename "$ORG_JSON_PATH")
UPDATE_JSON_PATH=$(echo "$UPDATE_PB_PATH" | sed 's/.pb$//' | sed 's/$/.json/')

NAMESPACE=fabric-org1

source "${BASH_SOURCE%/*}/channel-utils.sh"

pod="$(select_first_cli_pod org1 0)"

WORKDIR=/opt/blocks/compute-org-update-$(date +%s)
echo "WORKDIR=$WORKDIR"

pod_exec "$pod" mkdir -p $WORKDIR

kubectl -n "$NAMESPACE" cp "$ORG_JSON_PATH" "$pod:$WORKDIR/$ORG_JSON_NAME"

pod_exec "$pod" peer channel fetch config "$WORKDIR/config_block.pb" -o "$ORDERER" -c "$CHANNEL" --tls --cafile "$ORDERER_CA"

pod_exec "$pod" sh -c "configtxlator proto_decode --input '$WORKDIR/config_block.pb' --type common.Block | jq .data.data[0].payload.data.config > '$WORKDIR/config.json'"

set +e
read -r -d '' JQ_MERGE <<EOF
.[0] * {
    "channel_group": {
        "groups": {
            "Application": {
                "groups": {
                    "$ORG_MSP": .[1]
                }
            }
        }
    }
}
EOF
set -e

pod_exec "$pod" sh -c "jq -s '$JQ_MERGE' '$WORKDIR/config.json' '$WORKDIR/$ORG_JSON_NAME' > '$WORKDIR/modified_config.json'"

pod_exec "$pod" configtxlator proto_encode --input "$WORKDIR/config.json" --type common.Config --output "$WORKDIR/config.pb"

pod_exec "$pod" configtxlator proto_encode --input "$WORKDIR/modified_config.json" --type common.Config --output "$WORKDIR/modified_config.pb"

pod_exec "$pod" configtxlator compute_update --channel_id "$CHANNEL" --original "$WORKDIR/config.pb" --updated "$WORKDIR/modified_config.pb" --output "$WORKDIR/update.pb"

pod_exec "$pod" sh -c "configtxlator proto_decode --input '$WORKDIR/update.pb' --type common.ConfigUpdate | jq . > '$WORKDIR/update.json'"

kubectl -n "$NAMESPACE" cp "$pod:$WORKDIR/update.json" "$UPDATE_JSON_PATH"

set +e
read -r -d '' UPDATE_IN_ENVELOPE <<EOF
{
    "payload": {
        "header": {
            "channel_header": {
                "channel_id": "$CHANNEL",
                "type": 2
            }
        },
        "data": {
            "config_update": $(cat "$UPDATE_JSON_PATH")
        }
    }
}
EOF
set -e

pod_exec "$pod" sh -c "echo '$UPDATE_IN_ENVELOPE' | jq . > '$WORKDIR/update_in_envelope.json'"

pod_exec "$pod" configtxlator proto_encode --input "$WORKDIR/update_in_envelope.json" --type common.Envelope --output "$WORKDIR/update_in_envelope.pb"

kubectl -n "$NAMESPACE" cp "$pod:$WORKDIR/update_in_envelope.pb" "$UPDATE_PB_PATH"
