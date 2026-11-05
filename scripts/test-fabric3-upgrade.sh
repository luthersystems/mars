#!/usr/bin/env bash
#
# End-to-end test of the Fabric 3 upgrade scripts (luthersystems/mars#341)
# against a real Fabric network on this machine: 3 Raft orderers and 1 peer,
# started from the release binaries, with a system channel like our deployed
# networks (byfn-sys-channel, channel luther).
#
# Why a real network: the scripts drive osnadmin, config updates and Raft,
# whose behaviour a stub would only restate. A fake kubectl stands in for the
# cluster: "exec" runs the command here, with the identity and TLS files of
# the pod it names, and "get" answers from the deployment state this test
# keeps (image tag and env per orderer and peer).
#
# It runs the runbook (docs/fabric-3-upgrade.md):
#   0. pre-flight on 2.5: system channel present
#   1. orderers restarted, one at a time, with channel participation on
#   2. remove-system-channel.sh (dry run, real run, run again)
#   3. orderers then the peer on the new release, one at a time
# then runs the k8s_fabric_upgrade_v3 role (when ansible-playbook is on
# PATH), and checks after each step that the channel orders and the peer commits.
# It also checks the two failures the runbook warns of: a 3.x orderer stops at
# startup on a ledger with a system channel, and with BootstrapMethod "file".
#
# Usage: scripts/test-fabric3-upgrade.sh
#   FABRIC_FROM (default 2.5.7), FABRIC_TO (default 3.1.5)
#   FABRIC_BIN_CACHE: where to keep the downloaded binaries
#   KEEP=true keeps the work directory
# Needs: bash, curl, jq; Linux amd64 or arm64. Optional: ansible-playbook.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPTS="$ROOT/ansible-roles/k8s_fabric_scripts/files/scripts"
FROM="${FABRIC_FROM:-2.5.7}"
TO="${FABRIC_TO:-3.1.5}"
WORK="$(mktemp -d)"
CACHE="${FABRIC_BIN_CACHE:-$WORK/cache}"
SYS=byfn-sys-channel
CH=luther
N=3
DOMAIN=luther.systems
PIDS=()

cleanup() {
    local p
    for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done
    wait 2>/dev/null || true
    if [[ "${KEEP:-false}" == true ]]; then
        echo "work directory: $WORK"
    else
        rm -rf "$WORK"
    fi
}
trap cleanup EXIT

fail() {
    echo "FAIL: $*" >&2
    for f in "$WORK"/logs/*.log; do
        echo "--- tail $f" >&2
        tail -n 20 "$f" >&2 || true
    done
    exit 1
}
ok() { echo "OK:   $*"; }

arch="$(uname -m)"
case "$arch" in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac

fetch() { # fetch <version> -> $CACHE/<version>/bin
    local v="$1"
    [[ -x "$CACHE/$v/bin/orderer" ]] && return
    mkdir -p "$CACHE/$v"
    curl -fsSL "https://github.com/hyperledger/fabric/releases/download/v$v/hyperledger-fabric-linux-$arch-$v.tar.gz" |
        tar xz -C "$CACHE/$v"
}
fetch "$FROM"
fetch "$TO"
B_FROM="$CACHE/$FROM/bin"
B_TO="$CACHE/$TO/bin"

mkdir -p "$WORK"/{logs,k8s,art,data}
cd "$WORK"

# Crypto material and artifacts, as cryptogen and configtxgen make them for
# our networks. localhost and 127.0.0.1 are in every TLS certificate, so the
# fake kubectl can point the pods' host names at this machine.
{
    echo "OrdererOrgs:"
    echo "  - Name: Orderer"
    echo "    Domain: $DOMAIN"
    echo "    Specs:"
    for i in $(seq 0 $((N - 1))); do
        echo "      - Hostname: orderer$i"
        echo "        SANS: [localhost, 127.0.0.1]"
    done
    echo "PeerOrgs:"
    echo "  - Name: Org1"
    echo "    Domain: org1.$DOMAIN"
    echo "    EnableNodeOUs: true"
    echo "    Template: {Count: 1, SANS: [localhost, 127.0.0.1]}"
    echo "    Users: {Count: 1}"
} >crypto-config.yaml
"$B_FROM/cryptogen" generate --config crypto-config.yaml >/dev/null
OC="$WORK/crypto-config/ordererOrganizations/$DOMAIN"
PC="$WORK/crypto-config/peerOrganizations/org1.$DOMAIN"

consenters=""
addresses=""
for i in $(seq 0 $((N - 1))); do
    crt="$OC/orderers/orderer$i.$DOMAIN/tls/server.crt"
    consenters+="      - {Host: localhost, Port: $((7050 + 100 * i)), ClientTLSCert: $crt, ServerTLSCert: $crt}"$'\n'
    addresses+="localhost:$((7050 + 100 * i)), "
done
cat >configtx.yaml <<EOF
Organizations:
  - &OrdererOrg
    Name: OrdererOrg
    ID: OrdererMSP
    MSPDir: $OC/msp
    Policies:
      Readers: {Type: Signature, Rule: "OR('OrdererMSP.member')"}
      Writers: {Type: Signature, Rule: "OR('OrdererMSP.member')"}
      Admins: {Type: Signature, Rule: "OR('OrdererMSP.admin')"}
  - &Org1
    Name: Org1MSP
    ID: Org1MSP
    MSPDir: $PC/msp
    Policies:
      Readers: {Type: Signature, Rule: "OR('Org1MSP.admin', 'Org1MSP.peer', 'Org1MSP.client')"}
      Writers: {Type: Signature, Rule: "OR('Org1MSP.admin', 'Org1MSP.client')"}
      Admins: {Type: Signature, Rule: "OR('Org1MSP.admin')"}
      Endorsement: {Type: Signature, Rule: "OR('Org1MSP.peer')"}
Orderer: &OrdererDefaults
  OrdererType: etcdraft
  Addresses: [${addresses%, }]
  EtcdRaft:
    Consenters:
$consenters
  BatchTimeout: 1s
  BatchSize: {MaxMessageCount: 10, AbsoluteMaxBytes: 99 MB, PreferredMaxBytes: 512 KB}
  Organizations:
  Policies:
    Readers: {Type: ImplicitMeta, Rule: "ANY Readers"}
    Writers: {Type: ImplicitMeta, Rule: "ANY Writers"}
    Admins: {Type: ImplicitMeta, Rule: "MAJORITY Admins"}
    BlockValidation: {Type: ImplicitMeta, Rule: "ANY Writers"}
Channel: &ChannelDefaults
  Policies:
    Readers: {Type: ImplicitMeta, Rule: "ANY Readers"}
    Writers: {Type: ImplicitMeta, Rule: "ANY Writers"}
    Admins: {Type: ImplicitMeta, Rule: "MAJORITY Admins"}
  Capabilities: {V2_0: true}
Profiles:
  Genesis:
    <<: *ChannelDefaults
    Orderer:
      <<: *OrdererDefaults
      Organizations: [*OrdererOrg]
      Capabilities: {V2_0: true}
    Consortiums:
      SampleConsortium:
        Organizations: [*Org1]
  Channel:
    Consortium: SampleConsortium
    <<: *ChannelDefaults
    Application:
      Organizations: [*Org1]
      Capabilities: {V2_0: true}
      Policies:
        Readers: {Type: ImplicitMeta, Rule: "ANY Readers"}
        Writers: {Type: ImplicitMeta, Rule: "ANY Writers"}
        Admins: {Type: ImplicitMeta, Rule: "MAJORITY Admins"}
        LifecycleEndorsement: {Type: ImplicitMeta, Rule: "MAJORITY Endorsement"}
        Endorsement: {Type: ImplicitMeta, Rule: "MAJORITY Endorsement"}
EOF
"$B_FROM/configtxgen" -configPath . -profile Genesis -channelID "$SYS" -outputBlock art/genesis.block 2>/dev/null
"$B_FROM/configtxgen" -configPath . -profile Channel -channelID "$CH" -outputCreateChannelTx art/channel.tx 2>/dev/null

# ---- nodes ---------------------------------------------------------------

declare -A NODE_PID

# orderer_env <i> <cp:true|false>: the env the fabric-orderer chart sets,
# with local paths and ports.
orderer_env() {
    local i="$1" cp="$2" d="$OC/orderers/orderer$1.$DOMAIN"
    echo "FABRIC_LOGGING_SPEC=INFO"
    echo "ORDERER_OPERATIONS_LISTENADDRESS=127.0.0.1:$((18443 + 100 * i))"
    echo "ORDERER_GENERAL_LISTENADDRESS=127.0.0.1"
    echo "ORDERER_GENERAL_LISTENPORT=$((7050 + 100 * i))"
    echo "ORDERER_GENERAL_LOCALMSPDIR=$d/msp"
    echo "ORDERER_GENERAL_LOCALMSPID=OrdererMSP"
    echo "ORDERER_GENERAL_TLS_ENABLED=true"
    echo "ORDERER_GENERAL_TLS_CERTIFICATE=$d/tls/server.crt"
    echo "ORDERER_GENERAL_TLS_PRIVATEKEY=$d/tls/server.key"
    echo "ORDERER_GENERAL_TLS_ROOTCAS=[$d/tls/ca.crt]"
    echo "ORDERER_GENERAL_CLUSTER_CLIENTCERTIFICATE=$d/tls/server.crt"
    echo "ORDERER_GENERAL_CLUSTER_CLIENTPRIVATEKEY=$d/tls/server.key"
    echo "ORDERER_GENERAL_CLUSTER_ROOTCAS=[$d/tls/ca.crt]"
    echo "ORDERER_FILELEDGER_LOCATION=$WORK/data/o$i"
    echo "ORDERER_CONSENSUS_WALDIR=$WORK/data/o$i/etcdraft/wal"
    echo "ORDERER_CONSENSUS_SNAPDIR=$WORK/data/o$i/etcdraft/snapshot"
    # Each orderer has its own admin port. Fabric 2.5 starts the admin
    # server (default 127.0.0.1:9443) even with channel participation off;
    # in a pod that address is the pod's own, here it is shared.
    echo "ORDERER_ADMIN_LISTENADDRESS=127.0.0.1:$((7053 + 100 * i))"
    if [[ "$cp" == true ]]; then
        echo "ORDERER_GENERAL_BOOTSTRAPMETHOD=none"
        echo "ORDERER_CHANNELPARTICIPATION_ENABLED=true"
        echo "ORDERER_ADMIN_TLS_ENABLED=true"
        echo "ORDERER_ADMIN_TLS_CERTIFICATE=$d/tls/server.crt"
        echo "ORDERER_ADMIN_TLS_PRIVATEKEY=$d/tls/server.key"
        echo "ORDERER_ADMIN_TLS_ROOTCAS=[$d/tls/ca.crt]"
        echo "ORDERER_ADMIN_TLS_CLIENTAUTHREQUIRED=true"
        echo "ORDERER_ADMIN_TLS_CLIENTROOTCAS=[$d/tls/ca.crt]"
    else
        echo "ORDERER_GENERAL_GENESISFILE=$WORK/art/genesis.block"
        echo "ORDERER_GENERAL_GENESISMETHOD=file"
    fi
}

# record_deployment <namespace> <name> <container> <version> <env file>:
# what "kubectl get deployment" answers.
record_deployment() {
    mkdir -p "$WORK/k8s/$1"
    jq -n --arg c "$3" --arg img "hyperledger/$3:$4" --rawfile env "$5" '{
        metadata: {name: $ARGS.positional[0]},
        spec: {template: {spec: {containers: [{name: $c, image: $img,
            env: ($env | split("\n") | map(select(length > 0) | split("=") |
                {name: .[0], value: (.[1:] | join("="))}))}]}}}}' \
        --args "$2" >"$WORK/k8s/$1/$2.json"
}

stop_node() { # stop_node <key>
    local p="${NODE_PID[$1]-}"
    [[ -n "$p" ]] || return 0
    kill "$p" 2>/dev/null || true
    wait "$p" 2>/dev/null || true
    unset "NODE_PID[$1]"
}

# start_orderer <i> <version> <cp>
start_orderer() {
    local i="$1" v="$2" cp="$3" envf="$WORK/k8s/o$1.env"
    stop_node "o$i"
    orderer_env "$i" "$cp" >"$envf"
    record_deployment fabric-orderer "fabric-orderer$i" fabric-orderer "$v" "$envf"
    (
        set -a
        # shellcheck disable=SC1090
        . "$envf"
        exec env FABRIC_CFG_PATH="$CACHE/$v/config" "$CACHE/$v/bin/orderer"
    ) >>"$WORK/logs/orderer$i.log" 2>&1 &
    NODE_PID[o$i]=$!
    PIDS+=($!)
    wait_http "127.0.0.1:$((18443 + 100 * i))" "orderer$i"
}

peer_env() {
    local d="$PC/peers/peer0.org1.$DOMAIN"
    echo "FABRIC_LOGGING_SPEC=INFO"
    echo "CORE_PEER_ID=peer0.org1.$DOMAIN"
    echo "CORE_PEER_ADDRESS=localhost:7051"
    echo "CORE_PEER_LISTENADDRESS=127.0.0.1:7051"
    echo "CORE_PEER_CHAINCODEADDRESS=127.0.0.1:7052"
    echo "CORE_PEER_CHAINCODELISTENADDRESS=127.0.0.1:7052"
    echo "CORE_PEER_GOSSIP_BOOTSTRAP=localhost:7051"
    echo "CORE_PEER_GOSSIP_EXTERNALENDPOINT=localhost:7051"
    echo "CORE_PEER_LOCALMSPID=Org1MSP"
    echo "CORE_PEER_MSPCONFIGPATH=$d/msp"
    echo "CORE_PEER_TLS_ENABLED=true"
    echo "CORE_PEER_TLS_CERT_FILE=$d/tls/server.crt"
    echo "CORE_PEER_TLS_KEY_FILE=$d/tls/server.key"
    echo "CORE_PEER_TLS_ROOTCERT_FILE=$d/tls/ca.crt"
    echo "CORE_PEER_FILESYSTEMPATH=$WORK/data/peer"
    echo "CORE_OPERATIONS_LISTENADDRESS=127.0.0.1:19443"
    echo "CORE_VM_ENDPOINT="
}

# mars_core <version>: a directory with the core.yaml that the
# k8s_fabric_peer role renders for a peer of that version (chaincode as a
# service), so the 3.x peer runs the mars config with runtimeParams. Without
# ansible-playbook it prints the release's own config directory. The env of
# peer_env overrides the addresses and paths in it.
mars_core() {
    local v="$1" d="$WORK/core-$1"
    if ! command -v ansible-playbook >/dev/null; then
        echo "$CACHE/$v/config"
        return
    fi
    mkdir -p "$d"
    cat >"$d/render.yml" <<YAML
- hosts: localhost
  connection: local
  gather_facts: false
  vars_files:
    - $ROOT/ansible-roles/k8s_fabric_peer/defaults/main.yml
  tasks:
    - template:
        src: $ROOT/ansible-roles/k8s_fabric_peer/templates/configmap.yaml.j2
        dest: $d/configmap.yaml
    - copy:
        content: "{{ (lookup('file', '$d/configmap.yaml') | from_yaml).data['core.yaml'] }}"
        dest: $d/core.yaml
YAML
    # Extra vars: the role defaults in vars_files would win over play vars.
    ansible-playbook -i localhost, "$d/render.yml" -e "k8s_fabric_version=$v" \
        -e "k8s_fabric_network_domain_root=$DOMAIN kubectl_asset_root_path=$d" \
        -e "luther_env=test luther_buildenv_images_version=v0.0.0" \
        -e '{"k8s_fabric_peer_chaincode_ccaas": true}' >"$WORK/logs/render-core-$v.log" 2>&1 ||
        fail "rendering the mars core.yaml for $v"
    echo "$d"
}

start_peer() { # start_peer <version>
    local v="$1" envf="$WORK/k8s/peer.env" cfg
    stop_node peer
    peer_env >"$envf"
    record_deployment fabric-org1 fabric-peer0-org1 fabric-peer "$v" "$envf"
    cfg="$(mars_core "$v")"
    (
        set -a
        # shellcheck disable=SC1090
        . "$envf"
        exec env FABRIC_CFG_PATH="$cfg" "$CACHE/$v/bin/peer" node start
    ) >>"$WORK/logs/peer.log" 2>&1 &
    NODE_PID[peer]=$!
    PIDS+=($!)
    wait_port 7051 peer
}

wait_port() { # wait_port <port> <name>: until the port accepts connections
    for _ in $(seq 1 60); do
        (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null && return
        sleep 1
    done
    fail "$2 did not come up"
}

wait_http() { # wait_http <addr> <name>: until /healthz answers
    for _ in $(seq 1 60); do
        curl -fs "http://$1/healthz" >/dev/null 2>&1 && return
        sleep 1
    done
    fail "$2 did not come up"
}

# ---- fake kubectl ---------------------------------------------------------

# Pods: fabric-cli<i>-orderer-x (orderer org admin, orderer<i> TLS),
# fabric-cli0-org1-x (org1 admin), fabric-peer0-org1-x (the peer).
# /etc/hyperledger/fabric is each pod's own directory here.
for i in $(seq 0 $((N - 1))); do
    p="$WORK/pods/fabric-cli$i-orderer-x"
    mkdir -p "$p/orderertls"
    cp -r "$OC/orderers/orderer$i.$DOMAIN/tls" "$p/tls"
    cp "$OC/tlsca/tlsca.$DOMAIN-cert.pem" "$p/orderertls/"
done
p="$WORK/pods/fabric-cli0-org1-x"
mkdir -p "$p/orderertls"
cp -r "$PC/peers/peer0.org1.$DOMAIN/tls" "$p/tls"
cp "$OC/tlsca/tlsca.$DOMAIN-cert.pem" "$p/orderertls/"
mkdir -p "$WORK/pods/fabric-peer0-org1-x"

mkdir -p "$WORK/bin"
cat >"$WORK/bin/kubectl" <<'KUBECTL'
#!/usr/bin/env bash
set -euo pipefail
ns=""
args=()
while [[ $# -gt 0 ]]; do
    case "$1" in
    -n) ns="$2"; shift 2 ;;
    *) args+=("$1"); shift ;;
    esac
done
set -- "${args[@]}"
verb="$1"; shift
case "$verb" in
get)
    kind="$1"; shift
    sel=""; out=""; name=""
    while [[ $# -gt 0 ]]; do
        case "$1" in
        -l) sel="$2"; shift 2 ;;
        -o) out="$2"; shift 2 ;;
        *) name="$1"; shift ;;
        esac
    done
    case "$kind" in
    pods)
        comp="$(grep -o 'component=[a-z]*' <<<"$sel" | cut -d= -f2)"
        org="$(grep -o 'organization=[a-z0-9]*' <<<"$sel" | cut -d= -f2)"
        idx="$(grep -o 'organization-index=[0-9]*' <<<"$sel" | cut -d= -f2 || true)"
        for d in "$FAKE_WORK"/pods/*; do
            pn="$(basename "$d")"
            case "$comp" in
            bccli) [[ "$pn" == fabric-cli${idx:-[0-9]}*-"$org"-x ]] || continue ;;
            bcpeer) [[ "$pn" == fabric-peer${idx:-[0-9]}*-"$org"-x ]] || continue ;;
            *) continue ;;
            esac
            echo "pod/$pn"
        done
        ;;
    deployment | deployments)
        if [[ -n "$name" ]]; then
            f="$FAKE_WORK/k8s/$ns/$name.json"
            [[ -f "$f" ]] || { echo "Error from server (NotFound)" >&2; exit 1; }
            cat "$f"
        else
            files=("$FAKE_WORK/k8s/$ns"/*.json)
            [[ -e "${files[0]}" ]] || files=()
            if [[ "$out" == name ]]; then
                for f in "${files[@]}"; do echo "deployment.apps/$(basename "$f" .json)"; done
            else
                jq -s '{items: .}' "${files[@]}" /dev/null
            fi
        fi
        ;;
    *) echo "fake kubectl: get $kind" >&2; exit 1 ;;
    esac
    ;;
exec)
    [[ "$1" == -i ]] && shift
    pod="$1"; shift
    [[ "$1" == -- ]] && shift
    pdir="$FAKE_WORK/pods/$pod"
    [[ -d "$pdir" ]] || { echo "fake kubectl: no pod $pod" >&2; exit 1; }
    # Point the pod's paths and host names at this machine.
    rewrite() {
        local s="$1" i
        s="${s//\/etc\/hyperledger\/fabric/$pdir}"
        for i in 0 1 2; do
            s="${s//orderer$i.luther.systems:7050/localhost:$((7050 + 100 * i))}"
            s="${s//orderer$i:7053/127.0.0.1:$((7053 + 100 * i))}"
        done
        printf '%s' "$s"
    }
    cmd=()
    for a in "$@"; do cmd+=("$(rewrite "$a")"); done
    case "$pod" in
    fabric-cli*-orderer-x)
        export CORE_PEER_LOCALMSPID=OrdererMSP
        export CORE_PEER_MSPCONFIGPATH="$FAKE_OC/users/Admin@luther.systems/msp"
        bin="$FAKE_CLI_BIN" ;;
    fabric-cli*-org1-x)
        export CORE_PEER_LOCALMSPID=Org1MSP
        export CORE_PEER_MSPCONFIGPATH="$FAKE_PC/users/Admin@org1.luther.systems/msp"
        bin="$FAKE_CLI_BIN" ;;
    fabric-peer*)
        set -a; . "$FAKE_WORK/k8s/peer.env"; set +a
        bin="$(cat "$FAKE_WORK/k8s/peer.bin")" ;;
    esac
    export CORE_PEER_ADDRESS=localhost:7051 CORE_PEER_TLS_ENABLED=true
    export CORE_PEER_TLS_ROOTCERT_FILE="$FAKE_PC/peers/peer0.org1.luther.systems/tls/ca.crt"
    export FABRIC_CFG_PATH="$FAKE_CLI_CFG"
    export PATH="$bin:$PATH"
    exec "${cmd[@]}"
    ;;
*) echo "fake kubectl: $verb" >&2; exit 1 ;;
esac
KUBECTL
chmod +x "$WORK/bin/kubectl"
export PATH="$WORK/bin:$PATH"
export FAKE_WORK="$WORK" FAKE_OC="$OC" FAKE_PC="$PC"
# The CLI pods run the 2.5 tools image (luthersystems/fabric-tools) through
# the whole upgrade, as on our clusters.
export FAKE_CLI_BIN="$B_FROM" FAKE_CLI_CFG="$CACHE/$FROM/config"

# The role's environment for the scripts.
export ORDERERC=$N PEER_ORGS=org1 CHANNEL=$CH FABRIC_DOMAIN=$DOMAIN \
    FABRIC_ORDERER_CLIENT_AUTH=false ORDERER_NAMESPACE=fabric-orderer WAIT_SECONDS=120

cli() { # cli <pod> <cmd...>: run as a pod does
    kubectl -n x exec "$@"
}

height_peer() {
    kubectl -n fabric-org1 exec fabric-peer0-org1-x -- peer channel getinfo -c "$CH" 2>/dev/null |
        grep -Eo '\{[^}]*\}' | jq '.height'
}

# config_bump <orderer index>: an orderer config update on luther (the batch
# timeout), signed by the orderer admin, so the channel orders a block.
bump=1
config_bump() {
    local i="$1" pod=fabric-cli0-orderer-x d="$WORK/upd"
    rm -rf "$d"; mkdir -p "$d"
    cli "$pod" -- peer channel fetch config "$d/c.pb" -c "$CH" -o "orderer$i.$DOMAIN:7050" \
        --tls --cafile "/etc/hyperledger/fabric/orderertls/tlsca.$DOMAIN-cert.pem" >/dev/null 2>&1 || return 1
    "$B_FROM/configtxlator" proto_decode --type common.Block --input "$d/c.pb" |
        jq -e '.data.data[0].payload.data.config' >"$d/c.json" || return 1
    bump=$((bump + 1))
    jq --arg t "${bump}s" '.channel_group.groups.Orderer.values.BatchTimeout.value.timeout = $t' "$d/c.json" >"$d/m.json"
    "$B_FROM/configtxlator" proto_encode --type common.Config --input "$d/c.json" --output "$d/c2.pb"
    "$B_FROM/configtxlator" proto_encode --type common.Config --input "$d/m.json" --output "$d/m.pb"
    "$B_FROM/configtxlator" compute_update --channel_id "$CH" --original "$d/c2.pb" --updated "$d/m.pb" --output "$d/u.pb" || return 1
    "$B_FROM/configtxlator" proto_decode --type common.ConfigUpdate --input "$d/u.pb" |
        jq --arg ch "$CH" '{payload: {header: {channel_header: {channel_id: $ch, type: 2}}, data: {config_update: .}}}' >"$d/e.json"
    "$B_FROM/configtxlator" proto_encode --type common.Envelope --input "$d/e.json" --output "$d/e.pb"
    cli "$pod" -- peer channel update -f "$d/e.pb" -c "$CH" -o "orderer$i.$DOMAIN:7050" \
        --tls --cafile "/etc/hyperledger/fabric/orderertls/tlsca.$DOMAIN-cert.pem" >/dev/null 2>&1
}

# expect_commit <what>: order a block and wait for the peer to commit it.
# Just after an orderer restarts, the other orderers may still forward to it
# as the old Raft leader (SERVICE_UNAVAILABLE: EOF), or it may not serve the
# channel yet. So it sends the update again, through each orderer in turn,
# until a block commits or 120 seconds pass.
expect_commit() {
    local before after i deadline=$((SECONDS + 120))
    before="$(height_peer)"
    while ((SECONDS < deadline)); do
        for i in $(seq 0 $((N - 1))); do
            config_bump "$i" && break
        done
        for _ in $(seq 1 10); do
            after="$(height_peer || echo 0)"
            if ((after > before)); then
                ok "$1: the peer committed block $((after - 1))"
                return
            fi
            sleep 1
        done
    done
    fail "$1: the peer is still at height $before"
}

# ---- 2.5 network with a system channel -----------------------------------

echo "$B_FROM" >"$WORK/k8s/peer.bin"
for i in $(seq 0 $((N - 1))); do start_orderer "$i" "$FROM" false; done
start_peer "$FROM"
# create-channel.sh does this on our networks. Raft may still be electing a
# leader, so retry.
retry() { # retry <log> <cmd...>
    local log="$1"
    shift
    for _ in $(seq 1 30); do
        "$@" >>"$WORK/logs/$log.log" 2>&1 && return
        sleep 1
    done
    fail "$log"
}
retry channel-create cli fabric-cli0-org1-x -- peer channel create -c "$CH" -f "$WORK/art/channel.tx" \
    -o "orderer0.$DOMAIN:7050" --tls --cafile "/etc/hyperledger/fabric/orderertls/tlsca.$DOMAIN-cert.pem" \
    --outputBlock "$WORK/art/$CH.block"
retry channel-join cli fabric-cli0-org1-x -- peer channel join -b "$WORK/art/$CH.block"
expect_commit "$FROM network with system channel $SYS"

preflight() { # preflight <step>: prints the report, returns its exit code
    STEP="$1" bash "$SCRIPTS/fabric3-preflight.sh" >"$WORK/preflight.out" 2>&1
}
row() { # row <check>: the report row of a check
    grep -E "^[A-Z]+ +$1 " "$WORK/preflight.out" || true
}

if preflight remove-system-channel; then fail "pre-flight passed with channel participation off"; fi
row 'orderer0 channel participation' | grep -q '^FAIL' || fail "pre-flight did not report channel participation off"
ok "pre-flight (step 2) blocks while channel participation is off"

# ---- a 3.x orderer stops on a ledger with a system channel ---------------

stop_node o2
cp -a "$WORK/data/o2" "$WORK/data/o2-copy"
start_orderer 2 "$FROM" false
for mode in file none; do
    (
        set -a
        # shellcheck disable=SC1090
        . <(orderer_env 2 "$([[ $mode == none ]] && echo true || echo false)" |
            sed "s!$WORK/data/o2!$WORK/data/o2-copy!; s!:7253!:7953!; s!:18643!:18943!; s!LISTENPORT=7250!LISTENPORT=7950!")
        timeout 30 env FABRIC_CFG_PATH="$CACHE/$TO/config" "$B_TO/orderer"
    ) >"$WORK/logs/negative-$mode.log" 2>&1 && fail "$TO orderer started with bootstrap $mode on a system channel ledger"
    want="$([[ $mode == none ]] && echo 'found system channel config block' || echo "Bootstrap method: 'file' is forbidden")"
    grep -q "$want" "$WORK/logs/negative-$mode.log" || fail "$TO orderer with bootstrap $mode: no \"$want\""
    ok "$TO orderer stops at startup with bootstrap $mode on a system channel ledger"
done

# ---- step 1: channel participation on, one orderer at a time --------------

for i in $(seq 0 $((N - 1))); do
    start_orderer "$i" "$FROM" true
    expect_commit "step 1: orderer$i restarted with channel participation"
done

preflight remove-system-channel || { cat "$WORK/preflight.out"; fail "pre-flight for step 2 failed"; }
ok "pre-flight READY for step 2"
if preflight upgrade; then cat "$WORK/preflight.out"; fail "pre-flight for step 3 passed with a system channel"; fi
row 'orderer0 system channel' | grep -q "$SYS" || fail "pre-flight does not name the system channel"
ok "pre-flight blocks step 3 while $SYS exists"

# ---- step 2: remove the system channel -------------------------------------

DRY_RUN=true bash "$SCRIPTS/remove-system-channel.sh" >"$WORK/logs/remove-dry.log" 2>&1 || fail "dry run failed"
preflight upgrade && fail "the dry run changed something"
ok "remove-system-channel.sh DRY_RUN changes nothing"

bash "$SCRIPTS/remove-system-channel.sh" >"$WORK/logs/remove.log" 2>&1 || { cat "$WORK/logs/remove.log"; fail "remove-system-channel.sh"; }
grep -q "is in maintenance mode" "$WORK/logs/remove.log" || fail "no maintenance mode"
ok "remove-system-channel.sh removed $SYS"
bash "$SCRIPTS/remove-system-channel.sh" >"$WORK/logs/remove-again.log" 2>&1 || fail "second run failed"
grep -q "nothing to do" "$WORK/logs/remove-again.log" || fail "second run did not skip"
ok "remove-system-channel.sh is safe to run again"
expect_commit "after step 2"
preflight upgrade || { cat "$WORK/preflight.out"; fail "pre-flight for step 3 failed"; }
ok "pre-flight READY for step 3"

# ---- step 3: orderers, then the peer, one at a time ------------------------

for i in $(seq 0 $((N - 1))); do
    start_orderer "$i" "$TO" true
    expect_commit "step 3: orderer$i on $TO"
done
echo "$B_TO" >"$WORK/k8s/peer.bin"
start_peer "$TO"
expect_commit "step 3: peer on $TO"
if command -v ansible-playbook >/dev/null; then
    grep -q 'useWriteBatch: true' "$WORK/core-$TO/core.yaml" || fail "the $TO core.yaml has no runtimeParams"
    grep -q 'runtimeParams' "$WORK/core-$FROM/core.yaml" && fail "the $FROM core.yaml has runtimeParams"
    ok "the $FROM and $TO peers ran the mars core.yaml; only $TO has runtimeParams"
fi
preflight upgrade || { cat "$WORK/preflight.out"; fail "pre-flight on $TO failed"; }
row 'orderer0 image' | grep -q "$TO" || fail "pre-flight does not see $TO"
ok "pre-flight READY on $TO"

# ---- the k8s_fabric_upgrade_v3 role runs the same scripts ------------------
# It checks the role's wiring (env names, exit codes) against this network.
# kubectl_configured and mars_local skip the kubectl and script upload tasks;
# the fake kubectl is on PATH.

if command -v ansible-playbook >/dev/null; then
    cat >"$WORK/role.yml" <<'YAML'
- hosts: localhost
  connection: local
  gather_facts: false
  roles:
    - role: k8s_fabric_upgrade_v3
YAML
    role() { # role <log> <extra vars as JSON>
        ANSIBLE_ROLES_PATH="$ROOT/ansible-roles" ansible-playbook -i localhost, "$WORK/role.yml" -e "$2" \
            -e "{\"kubectl_configured\": true, \"mars_local\": true, \"kubectl_env\": {},
                \"k8s_fabric_scripts_dir\": \"$SCRIPTS\", \"k8s_fabric_scripts_workdir\": \"$WORK\",
                \"k8s_fabric_orderer_cluster_size\": $N, \"k8s_fabric_peer_org_names\": [\"org1\"]}" \
            >"$WORK/logs/$1.log" 2>&1
    }
    role role-preflight '{"k8s_fabric_upgrade_v3_action": "preflight", "k8s_fabric_upgrade_v3_step": "upgrade"}' ||
        { cat "$WORK/logs/role-preflight.log"; fail "role pre-flight failed"; }
    grep -q READY "$WORK/logs/role-preflight.log" || fail "role pre-flight shows no report"
    role role-remove '{"k8s_fabric_upgrade_v3_action": "remove_system_channel"}' ||
        { cat "$WORK/logs/role-remove.log"; fail "role remove failed"; }
    grep -q "nothing to do" "$WORK/logs/role-remove.log" || fail "role remove did not skip"
    grep -q "changed=0" "$WORK/logs/role-remove.log" || fail "role remove reports a change"
    # A blocked step fails the play.
    role role-blocked '{"k8s_fabric_upgrade_v3_action": "preflight", "k8s_fabric_upgrade_v3_step": "upgrade",
        "k8s_fabric_upgrade_v3_channel": "missing"}' && fail "role pre-flight passed on a missing channel"
    grep -q "blocks upgrade" "$WORK/logs/role-blocked.log" || fail "role pre-flight failed for another reason"
    ok "the k8s_fabric_upgrade_v3 role runs the pre-flight check and the removal"
else
    echo "SKIP: ansible-playbook is not installed: the role is not tested"
fi

echo "PASS: Fabric $FROM to $TO upgrade path"
