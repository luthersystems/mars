#!/usr/bin/env bash

set -o xtrace
set -o errexit
set -o nounset
set -o pipefail

org_names="$1"
shift

org_size="$1"
shift

domain_root="$1"
shift

args=("$@")
head=("${@:1:$#-2}")
slst="${args[-2]}"
last="${args[-1]}"

ESC="- job_name: node
  proxy_url: http://monitoring-pushprox.monitoring:8080/
  static_configs:
    - targets:
"

for i in $org_names
do
  for j in $(seq 0 "$((org_size-1))")
  do
    ESC="$ESC""        - chaincode.peer$j.$i.$domain_root:9100
"
  done
done

exec helm "${head[@]}" --set prometheus.extraScrapeConfigs="$ESC" "$slst" "$last"
