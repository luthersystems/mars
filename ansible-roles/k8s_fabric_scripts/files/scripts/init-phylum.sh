#!/bin/bash

set -x

SUBSTRATE_VERSION="$1"
PHYLUM_VERSION="$2"
PHYLUM_VERSION_OLD="$3"

SHIROCLIENT=/opt/app
NAMESPACE=shiroclient-cli

source "${BASH_SOURCE%/*}/channel-utils.sh"

# TODO - refactor to use helper
# pod="$(select_first_pod org1 0)"
pod="$(kubectl -n $NAMESPACE get pod -o name | head -n 1 | sed 's!^pod/!!')"

# Check if the desired phylum is in service
pod_exec "$pod" \
    sh -c \
    "$SHIROCLIENT --config shiroclient.yaml --chaincode.version '$SUBSTRATE_VERSION' --phylum.version '$PHYLUM_VERSION_OLD' call get_phyla '{}'" \
    | jq -r '.phyla[] | select(.status == "IN_SERVICE") | .phylum_id' \
    | grep -Fx "$PHYLUM_VERSION"

if [[ $? -eq 0 ]]; then
    echo "Phylum version is initialized and in service"
    exit 0
fi

# Check if the desired phylum is out of service
pod_exec "$pod" \
    sh -c \
    "$SHIROCLIENT --config shiroclient.yaml --chaincode.version '$SUBSTRATE_VERSION' --phylum.version '$PHYLUM_VERSION_OLD' call get_phyla '{}'" \
    | jq -r '.phyla[] | select(.status != "IN_SERVICE") | .phylum_id' \
    | grep -Fx "$PHYLUM_VERSION"

if [[ $? -eq 0 ]]; then
    echo "Phylum version is not in service"
    exit 1
fi

# Load the phylum configuration file
pod_exec "$pod" \
    sh -c \
    "$SHIROCLIENT --config shiroclient.yaml --chaincode.version '$SUBSTRATE_VERSION' --phylum.version '$PHYLUM_VERSION_OLD' call set_app_control_property \"[\\\"bootstrap-cfg\\\", \\\"\$(cat /phylum/config.json.b64)\\\"]\""

if [[ $? -ne 0 ]]; then
    echo "Failed to load phylum config" >&2
    exit 1
fi

# Install the phylum
pod_exec "$pod" \
    sh -c \
    "$SHIROCLIENT --config shiroclient.yaml --client.tx-commit-timeout '$SHIRO_TX_COMMIT_TIMEOUT' --client.tx-timeout '$SHIRO_TX_TIMEOUT' --chaincode.version '$SUBSTRATE_VERSION' --phylum.version '$PHYLUM_VERSION_OLD' init --seed-size 4096 '$PHYLUM_VERSION' /phylum/phylum.zip"

if [[ $? -ne 0 ]]; then
    echo "Failed to upgrade phylum" >&2
    exit 1
fi
