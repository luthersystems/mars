#!/bin/bash

set -x

SUBSTRATE_VERSION="$1"
PHYLUM_VERSION="$2"
PHYLUM_VERSION_OLD="$3"

CHAINCODE=com_luthersystems_chaincode_substrate01
NAMESPACE=fabric-org1

source "${BASH_SOURCE%/*}/channel-utils.sh"

pod="$(select_first_pod org1 0)"

# SHIROCLIENT path changed in 2.58.0
SHIROCLIENT=/opt/app

# Test that the chaincode exists
pod_exec "$pod" \
    peer chaincode list --instantiated -C "$CHANNEL" \
    | egrep "\b$CHAINCODE\b"

EXISTS=$?

UPGRADE=0

if [[ "$EXISTS" -eq 0 ]]; then
    # Test that the correct chaincode version exists
    pod_exec "$pod" \
        peer chaincode list --instantiated -C "$CHANNEL" \
        | egrep "\b$CHAINCODE\b" \
        | sed 's/^.*[Vv]ersion:[[:space:]]*\([^[:space:],]*\).*$/\1/' \
        | grep -Fx "$SUBSTRATE_VERSION"
    UPGRADE=$?
fi

NAMESPACE=shiroclient-cli
pod="$(kubectl -n $NAMESPACE get pod -o name | head -n 1 | sed 's!^pod/!!')"

if [[ "$UPGRADE" -ne 0 ]]; then
    # substrate upgrade
    pod_exec "$pod" \
        $SHIROCLIENT \
        --config=shiroclient.yaml \
        --chaincode.version="$SUBSTRATE_VERSION" \
        upgrade
    if [[ $? -ne 0 ]]; then
        echo "Failed to upgrade chaincode" >&2
        exit 1
    fi
elif [[ "$EXISTS" -ne 0 ]]; then
    # bootstrap initialization
    pod_exec "$pod" \
        $SHIROCLIENT \
        --config=shiroclient.yaml \
        --chaincode.version="$SUBSTRATE_VERSION" \
        init -c --upgrade "bootstrap" '(defendpoint "init" () (route-success ()))'
    if [[ $? -ne 0 ]]; then
        echo "Failed to bootstrap chaincode" >&2
        exit 1
    fi
    PHYLUM_VERSION_OLD=bootstrap
fi

# List phyla for debugging
pod_exec "$pod" \
    sh -c \
    "$SHIROCLIENT --config shiroclient.yaml --chaincode.version '$SUBSTRATE_VERSION' --phylum.version '$PHYLUM_VERSION_OLD' call get_phyla '{}'"

if [[ $? -ne 0 ]]; then
    echo "Unable to list installed phyla" >&2
    exit 1
fi

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
    exit 0
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
    "$SHIROCLIENT --config shiroclient.yaml --client.tx-commit-timeout '$SHIRO_TX_COMMIT_TIMEOUT' --client.tx-timeout '$SHIRO_TX_TIMEOUT' --chaincode.version '$SUBSTRATE_VERSION' --phylum.version '$PHYLUM_VERSION_OLD' init --upgrade --seed-size 4096 '$PHYLUM_VERSION' /phylum/phylum.zip"

if [[ $? -ne 0 ]]; then
    echo "Failed to upgrade phylum" >&2
    exit 1
fi
