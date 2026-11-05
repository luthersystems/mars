# Fabric 2.5 to 3.x upgrade

This runbook moves a running network from Fabric 2.5 to Fabric 3.1 or later,
with Raft ordering. Issue: [mars#341](https://github.com/luthersystems/mars/issues/341).
Substrate side: [substrate#1034](https://github.com/luthersystems/substrate/issues/1034)
(upgrade path) and [substrate#1033](https://github.com/luthersystems/substrate/issues/1033)
(batched reads and writes).

## Why

| Reason | Detail |
|---|---|
| Support | Fabric 2.5 is the last 2.x line. Fixes go to 3.x. |
| Speed | A 3.x peer can send chaincode reads and writes in batches (`chaincode.runtimeParams`). Substrate uses this after substrate#1033. |
| BFT | Fabric 3 adds BFT ordering. We keep Raft. BFT is a later, separate change. |

## What Fabric 3 removes

A 3.x node does not start, or stops at startup, when the network uses one of
these. The pre-flight check finds each one before you change a node.

| Removed | Our networks | Action |
|---|---|---|
| Upgrade from a version before 2.5 | 2.5 | None. Other networks: upgrade to 2.5 first. |
| System channel | Each network has one (`byfn-sys-channel`). It only booted the orderers and created `luther` once. Nothing uses it at run time. | Steps 1 and 2 remove it. |
| Kafka and Solo ordering | Raft (`etcdraft`) | None. |
| Legacy chaincode lifecycle (`lscc`) | `_lifecycle` | None. The pre-flight check confirms it. |
| The `fabric-tools` image | The CLI pods use the 2.5 tools image | None. The 2.5 `peer`, `osnadmin` and `configtxlator` work against 3.x nodes. |

You do **not** need the `V3_0` channel capability. It is only for BFT.

## Steps

Do each step on the test network first. Do the next step only when the step
before it is done on every node. Each step has a playbook in
luthersystems/ui-infrastructure.

| Step | What | Playbook | Undo |
|---|---|---|---|
| 0 | Back up. Run the pre-flight check. | `fabric3_preflight.yaml` | Not needed |
| 1 | Restart the 2.5 orderers with channel participation on | `fabric_remove_system_channel.yaml` (first part) | Set the flag false and run `fabric_orderer.yaml` |
| 2 | Remove the system channel | `fabric_remove_system_channel.yaml` (second part) | **None**. Restore the orderer volumes from the backup. |
| 3 | Upgrade the orderers, then the peers, to 3.x | `fabric_upgrade.yaml` with the new version | Restore the volumes from the backup |
| 4 | Check the network and the apps | `fabric3_preflight.yaml`, health checks | Not needed |

### Step 0: back up and check

1. Take a snapshot of each orderer volume and each peer volume (EBS
   snapshots of the `fabric-orderer` and `fabric-org*` PVCs).
2. Run the pre-flight check with step `remove-system-channel`. It reads the
   network and changes nothing. Before step 1 it reports
   `orderer<N> channel participation` as FAIL: step 1 fixes that. Every other
   FAIL row is a problem to fix first.

The check (`fabric3-preflight.sh`, role `k8s_fabric_upgrade_v3`, action
`preflight`):

| Check | Blocks |
|---|---|
| Orderer and peer images are 2.5 or 3.x | Every step |
| Raft or BFT ordering, not in maintenance mode | Every step |
| Channel capabilities V2_0 or later, application V2_0 or V2_5 | Every step |
| Chaincode is on `_lifecycle`, none on `lscc` | Every step |
| `osnadmin` is in the orderer CLI pod | Step 2 |
| Each orderer runs with channel participation and bootstrap method `none` | Step 2 |
| No orderer has a system channel; each serves `luther` | Step 3 |
| Peers and orderers are at the same height | Warning only |
| Docker (`dind`) chaincode | Warning only; see "Docker chaincode" |

### Step 1: channel participation on the 2.5 orderers

Set `k8s_fabric_orderer_channel_participation: true` and roll the orderers.
The orderer chart then:

- starts each orderer with `General.BootstrapMethod: none`, so it no longer
  reads the genesis block file. It still loads the system channel from its
  ledger.
- turns on the admin endpoint (`osnadmin`, port 7053, mutual TLS with the
  orderer TLS CA).
- adds a ClusterIP Service `orderer<N>` for the admin port, so the orderer CLI
  pod can reach it. The admin port is never on the public load balancer.

The rolling restart (`is_fabric_upgrade`) does one orderer at a time. A
network with one orderer stops ordering for some seconds.

Set the flag in the environment's inventory (for ui-infrastructure,
`inventories/<env>/group_vars/all`) and keep it true from then on. With it
false, a later orderer run on 2.5 boots the orderers from the genesis file
again. `fabric_remove_system_channel.yaml` stops if the inventory does not set
it.

On a 3.x version, mars sets channel participation on by itself, and the role
stops with an error if you set it false.

### Step 2: remove the system channel

Run `remove-system-channel.sh` (role `k8s_fabric_upgrade_v3`, action
`remove_system_channel`). Run it with dry run first. It:

1. Puts the system channel into maintenance mode, signed as the orderer org
   admin in the orderer CLI pod. Then no one can create a channel through it.
2. On each orderer in turn, runs `osnadmin channel remove`. The orderer
   restarts its other channels, which takes a second or two. The script waits
   until that orderer serves `luther` again, as an active consenter, at its
   earlier height or more, before it goes to the next orderer. One at a time
   keeps Raft quorum.
3. Checks that no orderer has a system channel.

You can run it again. It skips each part that is done. If it stops at an
orderer, it does not go on to the next one; read that orderer's log.

There is no undo. A removed system channel does not come back except from a
volume restore.

Then run the pre-flight check with step `upgrade`. It must say READY.

### Step 3: upgrade to 3.x

Set the Fabric version (in ui-infrastructure, `k8s_fabric_version` and the
orderer and peer versions in `version.yaml`) to the 3.x release, and run
`fabric_upgrade.yaml`. It upgrades the orderers first, then the CA, then the
peers, one node at a time, and pauses the network around each node
(`upgrade-pauser.sh`).

On 3.x the peer `core.yaml` from mars adds:

```yaml
chaincode:
  runtimeParams:
    useWriteBatch: true
    maxSizeWriteBatch: 1000
    useGetMultipleKeys: true
    maxSizeGetMultipleKeys: 1000
```

Change these with `k8s_fabric_peer_chaincode_runtime_params`; set only the keys
you change, for example `{useWriteBatch: false}`. A 2.x peer
gets the same `core.yaml` as before.

### Step 4: check

1. Run the pre-flight check with step `upgrade`. It must say READY.
2. Check the oracle health endpoint (`/v1/ui/health_check`). Each service
   must be UP.
3. Do one write transaction through the app. shiroclient still uses the
   Fabric 2.x client SDK; this confirms that it endorses and commits on 3.x.

## Errors you can see

| Error | Cause | Fix |
|---|---|---|
| `Bootstrap method: 'file' is forbidden, since system channel is no longer supported` (3.x orderer stops) | Step 1 was not done | Roll back the orderer image to 2.5, do step 1 |
| `found system channel config block in the ledger ... Remove it before upgrading` (3.x orderer stops) | Step 2 was not done | Roll back the orderer image to 2.5, do step 2 |
| `osnadmin channel list failed` in step 2 | The orderer is not on channel participation, or the `orderer<N>` Service is missing | Do step 1 again |
| `VMEndpoint not set and no ExternalBuilders defined` (3.x peer stops) | The peer has no Docker endpoint and no external builder | Use chaincode as a service (`ccaas`), or configure the Docker endpoint |

## Docker chaincode

Fabric 3 peers that build chaincode with Docker use the upstream
`hyperledger/fabric-ccenv:<version>` image. buildenv has hardened images only
for 2.2, 2.4 and 2.5 ([buildenv#189](https://github.com/luthersystems/buildenv/issues/189)
adds 3.1). Networks with chaincode as a service (`ccaas`) are not
affected. The pre-flight check shows a warning for Docker chaincode.

## New networks

This runbook is for a running network. A new network on 3.x has no system
channel, so it must create `luther` with `osnadmin channel join` and a channel
genesis block. `create-channel.sh` uses `peer channel create`, which needs
the system channel. Until mars supports that ([mars#343](https://github.com/luthersystems/mars/issues/343)),
create a new network on 2.5 and upgrade it.

## Test

`scripts/test-fabric3-upgrade.sh` runs the full path on a local network (three
Raft orderers and one peer, the Fabric release binaries, a fake `kubectl`):

1. A 2.5.7 network with a system channel commits blocks.
2. The pre-flight check blocks step 2 while channel participation is off.
3. A 3.1.5 orderer stops on a ledger with a system channel, with both
   bootstrap methods.
4. Step 1 on each orderer, one at a time; the network commits after each.
5. `remove-system-channel.sh` in a dry run, a real run, and a second run.
6. Step 3: each orderer, then the peer, on 3.1.5, with the mars 3.x
   `core.yaml`; the network commits after each.
7. The pre-flight check says READY.

```bash
./scripts/test-fabric3-upgrade.sh            # FROM=2.5.7 TO=3.1.5
KEEP=true ./scripts/test-fabric3-upgrade.sh  # keep the work directory and logs
```
