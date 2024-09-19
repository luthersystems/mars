#!/usr/bin/env bash

set -o xtrace
set -o errexit
set -o nounset
set -o pipefail

# the following environment variables are expected:
# PAUSING_AT
# WHICH
# CHANNEL
# ORDERERC
# PEER_ORG
# FABRIC_DOMAIN

source "${BASH_SOURCE%/*}/channel-utils.sh"

function get_height_for_orderer() {
  PARSEABLE="$(kubectl -n "$THRUNAME" exec "$THRUPEER" -- peer channel fetch newest /dev/null -c "$CHANNEL" -o orderer"$1".${FABRIC_DOMAIN}:7050 --tls --cafile /opt/gopath/src/github.com/hyperledger/fabric/peer/orderertls/tlsca.${FABRIC_DOMAIN}-cert.pem 2>&1)"
  PARSEABLE="$(echo "$PARSEABLE" | egrep -o 'Received block: [0-9]+$')"
  HEIGHT_FOR_ORDERER="$(echo "$PARSEABLE" | cut -d " " -f 3)"
}

function get_height_for_peer() {
  PARSEABLE="$(kubectl -n "$THRUNAME" exec "$THRUPEER" -- peer channel getinfo -c "$CHANNEL")"
  PARSEABLE="$(echo "$PARSEABLE" | egrep -o '\{[^\}]*\}')"
  HEIGHT_FOR_PEER="$(echo "$PARSEABLE" | jq .height)"
}

function get_peak() {
  # determine where the highest orderer is at
  PEAK=0
  for i in $(seq 0 "$((ORDERERC - 1))"); do
    if [ "$i" != "$1" ]; then # support for excluding an orderer
      get_height_for_orderer "$i"
      if ((HEIGHT_FOR_ORDERER > PEAK)); then
        PEAK="$HEIGHT_FOR_ORDERER"
      fi
    fi
  done
}

function g_orderer() {
  THRUNAME="fabric-org1"
  THRUPEER="$(kubectl -n "$THRUNAME" get pod | egrep '^fabric-peer0-org1-' | cut -d " " -f 1)"

  get_peak "$WHICH"

  # wait until the orderer regains that level
  while [ 1 ]; do
    if ! get_height_for_orderer "$WHICH"; then
      sleep 3
      continue
    fi

    if ((HEIGHT_FOR_ORDERER >= PEAK)); then
      break
    fi

    sleep 3
  done
}

function g_peer() {
  THRUNAME="fabric-$PEER_ORG"
  THRUPEER="$(kubectl -n "$THRUNAME" get pod | egrep '^fabric-peer'"$WHICH"'-'"$PEER_ORG"'-' | cut -d " " -f 1)"

  while ! get_peak invalid; do
    sleep 3
  done

  # wait until the peer gains that level
  while [ 1 ]; do
    if ! get_height_for_peer; then
      sleep 3
      continue
    fi

    if ((HEIGHT_FOR_PEER >= PEAK)); then
      break
    fi

    sleep 3
  done
}

function g() {
  if [ "$PAUSING_AT" == "orderer" ]; then
    g_orderer
  elif [ "$PAUSING_AT" == "peer" ]; then
    g_peer
  else
    exit 1
  fi
}

g
