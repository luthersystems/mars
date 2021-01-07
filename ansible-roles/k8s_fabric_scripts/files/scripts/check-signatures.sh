#!/bin/bash

# Prints the signatures contained in a signed config update proposal
# (common.Envelope protobuf) so that the signer certificate can be validated.
# Useful for checking updates before using apply-update.sh or debugging
# policy issues when apply-update.sh fails.

set -xeuo pipefail

ORG_NAME=$1
PROPOSAL_PATH=$2

[ -e "$PROPOSAL_PATH" ]

NAMESPACE="fabric-$ORG_NAME"

source "${BASH_SOURCE%/*}/channel-utils.sh"

pod="$(select_first_pod "$ORG_NAME" 0)"

WORKDIR=/tmp/check-signatures-${RANDOM}
echo "WORKDIR=$WORKDIR"

REMOTE_PROPOSAL_PATH="$WORKDIR/signed_proposal.pb"
REMOTE_PROPOSAL_JSON="$WORKDIR/signed_proposal.json"

pod_exec "$pod" mkdir -p $WORKDIR

kubectl -n "$NAMESPACE" cp "$PROPOSAL_PATH" "$pod:$REMOTE_PROPOSAL_PATH"

pod_exec "$pod" configtxlator proto_decode --type common.Envelope --input "$REMOTE_PROPOSAL_PATH" --output "$REMOTE_PROPOSAL_JSON"
pod_exec "$pod" jq .payload.data.signatures "$REMOTE_PROPOSAL_JSON"
