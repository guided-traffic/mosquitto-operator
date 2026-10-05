# High availability — parked research

**Parked.** The first release is one broker run from Git
([ADR 0012](../adr/0012-the-first-release-is-one-broker-run-from-git-and-high-availability-is-parked.md));
everything highly-available or multi-replica waits here for the last phase of
[the project plan](project-plan.md), which starts only when every other phase is done.
This page is the research the decision will need then: what open-source Mosquitto can and cannot
do, how the field makes MQTT highly available, which move is open to this operator, and the
questions that have to be answered first (HA1–HA7 at the end). It is a planning document
([ADR 0011](../adr/0011-documentation-has-five-homes-and-tickets-are-work-lists-that-get-archived.md)
D10): nothing in code or in a record's `Decision` cites it, and it is consumed into records and
the plan's last phase when the work starts.

Until then `spec.replicas` keeps its meaning — independent brokers, no shared state — and nothing
built for the first release relies on `replicas > 1`.

Method: a web survey of broker architectures, operators and deployment patterns (sources at the
end; every claim not marked **measured** is a claim from a source), plus local measurements
against the pinned image (`eclipse-mosquitto:2.1.2-alpine`, docker 28.4.0, arm64, 2026-09-01),
numbered M10 and M11 to fit between the measurements of
[docs/developer/broker-behaviour.md](../developer/broker-behaviour.md).

## 0. The problem, honestly stated

**Open-source Mosquitto has no clustering.** No shared sessions, no shared retained messages, no
replication, no failover. This is not a gap in this operator's implementation; it is the upstream
product. The commercial edition sells clustering precisely because the open one does not have it.

So `replicas: 3` today is three brokers that do not know about each other:

- a retained message published through pod 0 does not exist on pod 1;
- a persistent session established on pod 1 is stranded there — the client reconnects, the Service
  sends it to pod 2, and its subscriptions and queued messages are gone;
- a subscriber on pod 0 never receives what a publisher sent to pod 2.

The Service in front of them makes this worse, not better, by hiding which broker a client got.

What is actually reachable is **one active broker with a bounded, predictable failover**, and the
size of that bound is a product decision (HA3), not an engineering one. Everything else — bridge
meshes, elected primaries — buys a specific property at a specific cost and should be built only
after somebody has said which property they need. HA is therefore **a promise to be narrowed and
then held**, and the first deliverable is the narrowing (HA1, HA2) rather than a mechanism.

The same honesty applies to upgrades. **"Without interrupting operation" is not achievable**: a
StatefulSet has no surge, so a version change stops the process and starts another; there is no
window where both serve. What is achievable is **no message loss and a bounded, measured
reconnect gap** — when the session and its queued messages survive on a PVC and the client
publishes at QoS 1 or 2. It does not hold for QoS 0, for clean sessions, or for clients that do not
reconnect, and no operator can fix those from this side (HA5).

## 1. The field: five models, and who uses which

Every production MQTT HA story found is one of these five. The split that matters runs between
the first two and the rest: **session takeover — a client reconnecting anywhere and finding its
subscriptions and queued messages — exists only where the broker itself cooperates.** Everything
else approximates availability while giving up session continuity, and the honest designs say so.

### M-A — Replicated state inside the broker cluster

The broker processes form a cluster and replicate subscription/session/retained state among
themselves.

| System | Mechanics | Notes |
|---|---|---|
| **EMQX 5.x** | Mria (Mnesia + RLOG): 3+ **core** nodes replicate synchronously and hold the full routing/session DB; stateless **replicant** nodes take client connections and forward writes to the core. | The reference K8s deployment: operator runs cores as a StatefulSet, replicants as a Deployment. Session takeover is supported and described as expensive ("a lot of messages to shuffle around"). |
| **VerneMQ** | Erlang/OTP full mesh, eventually consistent subscriber store, distinct TCP conns for message forwarding. | Netsplit behaviour is *configurable per operation* (`allow_register_during_netsplit` etc.) — they surface the CAP tradeoff instead of hiding it. |
| **Cedalo Pro Mosquitto** | Closed source. Raft consensus; two modes: Full-Sync (active/passive) and Dynamic-Security-Sync (active/active). | Their own K8s test: leader pod force-deleted, **one message lost, recovered in ~200 ms**. This is the bar for "real" Mosquitto HA — and it is the commercial product's reason to exist. |

### M-B — MQTT as a protocol adapter on a replicated log

The broker is not the store; a clustered log is, and MQTT semantics are reimplemented on top.

| System | Mechanics |
|---|---|
| **NATS** | Native MQTT listener since 2.2; sessions, retained messages and QoS are JetStream streams — Raft-replicated. An MQTT client talks to any node of a NATS cluster. |
| **TBMQ** (ThingsBoard) | Netty front, all session/subscription state in Kafka (+ Redis for device queues, Postgres for metadata). Every node identical, no leader, no coordinator; a standalone deployment "is simply a cluster of one". |

This model is architecturally the cleanest answer to MQTT HA — and it is unreachable from
Mosquitto, whose persistence interface (even the new 2.1 plugin one) is a local store, not a log.

### M-C — Warm standby and a traffic flip

Two (or more) complete brokers; something redirects clients when the active one dies. The
classic on-metal form is keepalived/VIP; the Kubernetes form is a Service selector flip.

The best documented open-source K8s example (Raymii): two Mosquitto pods on different nodes,
**bidirectionally bridged**, a watchdog pod polling readiness every 5s and patching the Service
selector on failure. Failover "within 5 seconds"; retained messages survive (the bridge copied
them); sessions do not; the watchdog itself is a stated SPOF.

That watchdog is, structurally, a hand-rolled operator. Which is the point: **in this repo, the
watchdog role already exists, leader-elected and HA on its own** — it is the operator.

### M-D — Bridge topologies

Mosquitto's native multi-broker feature. Bridges replicate *traffic* along configured topic
patterns — not sessions, not queues (measured, M11 below). Solid for edge aggregation and
fan-out across sites; as an HA primitive it only carries model M-C's state-sync duty.

Worth recording as evidence rather than opinion: the one serious attempt to build clustering
*into* Mosquitto — the `hui6075/mosquitto-cluster` fork (subscription broadcast, cluster
sessions, cluster retained) — is dead: **last push 2019-02-14** (GitHub API, read
2026-09-01), based on a 1.x broker. In-process Mosquitto clustering was tried and abandoned.

### M-E — Intelligence in the LB or the client

- **HAProxy ≥ 2.4 parses MQTT**: `stick on req.payload(0,0),mqtt_field_value(connect,client_identifier)`
  routes each client-id to the same backend. EMQX documents this to *avoid* session takeover
  cost; for non-clustered brokers it makes per-client sessions survive reconnects, as long as
  the broker set is stable. Broker death still loses that broker's sessions.
- **MQTT 5 server redirect**: CONNACK/DISCONNECT reason codes 0x9C "Use another server" /
  0x9D "Server moved" plus the Server Reference property. In the spec; **auto-follow support in
  mainstream client libraries could not be established in this survey** — treat it as a
  cooperative-fleet feature, not infrastructure.
- Multiple broker addresses in the client's reconnect list: the oldest trick, and the only one
  that costs the server side nothing.

---

## 2. The Kubernetes constraints any design must respect

- **RWO volume + node failure is the slow path.** The attach/detach controller waits its
  forced-detach timeout — **6 minutes** by default — before releasing a volume whose node went
  away, and StatefulSet at-most-one semantics keep the replacement Pending until then
  (kubernetes/kubernetes#112016 and the long tail of issues around it). A single-pod-with-PVC
  broker fails over in seconds for a *pod* crash and in minutes for a *node* loss — the exact
  case HA is bought for.
- **Detection has a ladder.** Container crash → kubelet restarts in seconds. Hung process →
  liveness probe. **Node loss → the control plane needs `node-monitor-grace-period` (40 s
  default) before pods on it even turn NotReady**, plus eviction settings. Anything faster
  requires an out-of-band prober.
- **The endpoint flip is the fast primitive.** Changing a Service selector / pod readiness
  propagates through EndpointSlice in ~a second. Kubernetes' equivalent of the keepalived VIP
  move — and unlike the VIP, it is exactly what an operator can drive.

---

## 3. Measured locally (M10, M11 — the gap in the numbering of the broker-behaviour page)

### M10 — A bridged pair replicates retained state, and it survives the primary's death

Two brokers, standby `B` bridging to primary `A`:

```
connection to-primary
address broker-a:1883
topic # both 1
try_private true
```

Sequence, all measured 2026-09-01:

1. `mosquitto_pub -h broker-a -t state/valve1 -m OPEN -r -q 1`
2. A **new** subscriber on `B` with `--retained-only` receives `state/valve1 OPEN` —
   the bridge forwarded the retain flag, `B` stored it as its own retained message.
3. `docker rm -f broker-a` — primary dead. A fresh subscriber on `B` still receives it.
4. `B` serves publish/subscribe normally.

So the standby of a bridged pair carries the retained state and is immediately serviceable.
(`try_private` is what makes retained propagation and loop detection work over the bridge, per
the mosquitto.conf man page; no loop was observed in these probes, which used a single
bridge definition with `both` direction on one side only.)

### M11 — Sessions and queued messages do NOT cross the bridge. The command is lost.

Same pair. A device opens a persistent session (`clean_session=false`, QoS 1 subscription on
`cmd/device-N`) against `A` and disconnects. A command is published QoS 1 while it is offline.

- Reconnect to **A** (no failover): `cmd/device-7 REBOOT` is delivered. Sessions work.
- Reconnect to **B** (failover): **nothing**. `B` has no session and no queue for the client.
  The bridge had even forwarded the live publish to `B` at publish time — but with no session
  there to hold it, it evaporated. **The command is lost for that client.**

This is the measured boundary of every non-clustered design: bridges replicate traffic,
never sessions. A failover with this mechanism is a `Session Present: false` moment for every
client, and anything queued for offline clients on the dead broker is gone.

---

## 4. The move that is open to us: the flip pair

Combine M-C and M-D, with the operator in the watchdog seat it already occupies:

```
                Service (selector: role=active)
                          │
            ┌─────────────┴─────────────┐
            v                           v   (not selected)
   ┌─────────────────┐  bridge  ┌─────────────────┐
   │ pod-0  PRIMARY  │<════════>│ pod-1  STANDBY  │
   │ own PVC         │ topic #  │ own PVC         │
   │ label role=active│ both 1  │ (label absent)  │
   └─────────────────┘          └─────────────────┘
            ^
            │ watch + probe + flip
       [ operator, leader-elected — already HA ]
```

- Both pods run the broker the whole time; each has its own PVC; the standby holds the bridge.
- The client Service selects on an operator-managed `role=active` pod label. Failover =
  the operator moves the label (and only new connections move with it).
- Detection: pod/readiness watch for the cheap cases; the node-loss case is honest —
  either accept the ~40 s control-plane bound, or the operator probes the active broker
  directly (a TCP/MQTT probe; post-authentication this wants the reserved operator principal
  of [ADR 0002](../adr/0002-the-metrics-exporter-is-written-here.md) and the reserved `mko-` prefix of [ADR 0013](../adr/0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md), which this design would reuse rather than invent).

What it delivers, measured or bounded, against HA1's option A (single pod + PVC):

| Property | Single + PVC (HA1 option A) | Flip pair |
|---|---|---|
| Pod crash | seconds (restart, same PVC) | seconds (flip) |
| **Node loss** | **~6 min** (RWO force-detach) | **detection + ~1 s flip** — no volume moves |
| Retained messages | survive (PVC) | survive (bridge, **measured M10**) |
| Persistent sessions + queued QoS | **survive** (PVC) | **lost on flip (measured M11)** |
| Split brain | impossible (one broker) | possible during partition; see below |
| Cost | 1× | 2× resources, bridge config, flip logic, fencing |

Neither dominates. They are two different promises, which is why the CRD field should be a
mode (`spec.ha.mode: none | standby`, working name), not a replica count — and it slots into
HA1/HA2 exactly where `topology` was proposed.

**The elegant part — the flip is also the upgrade.** Bring the standby up on the new image,
let the bridge carry the retained state across, flip, keep the old primary as the new standby.
The reconnect gap replaces the restart gap of HA5, and rollback is flipping back. This is
blue/green as the EMQX operator does it (`blueGreenUpdate`: `initialDelaySeconds`,
`evacuationStrategy.waitTakeover`, eviction rates) — their CRD is the API-shape precedent worth
copying. In-place restart stays the right strategy when session survival matters more than the
gap (the M11 tradeoff, made choosable: `upgradeStrategy: flip | restart`).

**What must be solved before this ships, named now:**

1. **Fencing.** The flip removes new connections from a sick primary; clients *already
   connected* to it stay connected to a broker that is no longer the one the Service names.
   With the bidirectional bridge alive their traffic still flows to the new active; in a
   partition it does not, and last-writer-wins on retained topics across the heal. Options:
   accept-and-document, or kill the old pod — and the ClusterRole deliberately holds **no
   `delete` on anything** ([ADR 0009](../adr/0009-delete-only-through-owner-references.md)), so
   fencing-by-delete is an authority extension that needs its own decision. → **HA6**.
2. **Bridge hygiene.** Bridge patterns must exclude `$SYS/#` and `$CONTROL/#`; once brokers
   require a login ([ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)) the bridge needs a credential — a second reserved principal under the `mko-` prefix.
3. **Bridge queueing bounds.** `cleansession false` on the bridge queues while the peer is
   down; the queue depth and `max_queued_messages` on both sides need explicit values, or a
   long partition turns into unbounded memory.

## 5. Alternatives weighed and parked

- **`persist-sqlite` + Litestream/LiteFS** (replicate the state file instead of the traffic):
  Litestream itself says it — disaster recovery, not HA. Async (roughly a second of loss
  window), takes over SQLite checkpointing, and v0.5.6/0.5.7 shipped a silent-failure bug with
  WAL reuse. Worth revisiting for a *backup* story; not a failover mechanism.
- **Active/active + HAProxy client-id stickiness** (M-E): keeps per-client sessions across
  reconnects while spreading load, needs full-mesh bridging for cross-broker pub/sub and an
  MQTT-aware LB in front (HAProxy ≥ 2.4). A refinement of HA1's option B for the
  semantics-limited workload class; parked until someone owns that workload.
- **Being honest about the ceiling:** if the requirement is session takeover — the M11 line —
  the answer is a broker from model M-A/M-B (EMQX, VerneMQ, NATS, TBMQ) or the commercial
  Mosquitto, and this operator's README should say exactly that sentence rather than imply the
  pair is a cluster.

## 6. Open questions when this is reopened

Each is put to the owner one at a time when the work starts, and each answer becomes a record
([ADR 0011](../adr/0011-documentation-has-five-homes-and-tickets-are-work-lists-that-get-archived.md) D10).
Answered questions of the first release that these depend on are records already: the reload
sidecar ([ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md)),
the reserved `mko-` prefix ([ADR 0013](../adr/0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md)),
and delete only through owner references ([ADR 0009](../adr/0009-delete-only-through-owner-references.md)).

### HA1 (P0) — Which HA shape is the product?

**What it blocks:** the meaning of `spec.replicas`, whether the operator does leader election,
the Service topology, the PVC layout, the whole storage story, and the first sentence of the
README.

| Option | What it gives | What it costs | Correct for |
|---|---|---|---|
| **A — Single active, fast failover.** `replicas: 1`, PVC, tuned probes, PDB, priorityClass, pre-pulled image. | Full MQTT semantics. Sessions and retained messages survive a pod move because they are on the PVC. One authority, no split brain. | A reschedule is a gap: RWO PVC detach plus scheduling plus broker start. Order of seconds to a minute, measurable, never zero. | Everything stateful: QoS 1/2, persistent sessions, retained messages. |
| **B — N independent brokers, one Service.** Today's shape. | Horizontal connection capacity. Survives one pod dying without a gap for *new* connections. | Not one broker. A retained message published through pod 0 is invisible on pod 1. A persistent session is stranded on whichever pod took it. A subscriber and a publisher that land on different pods never see each other. | QoS 0 fan-out with no retained messages and no persistent sessions — and only if every client accepts that. |
| **C — Bridge mesh.** N brokers, each bridging to the others. | Publishes propagate between brokers, so subscribers on different pods do see each other. | Retained and session state stay local anyway. Loop and duplicate risk. `bridge` reconnect storms. N*(N-1) connections. Every topic that must cross needs a topic-map entry. QoS across a bridge is per-hop, not end to end. | Fan-out across zones where duplicates are acceptable. |
| **D — Operator-elected primary.** N pods, only the elected one is in the Service selector; the rest are warm standbys. | Failover without waiting for scheduling. The operator controls the switch and can record it. | The standby holds no state unless storage is shared, and shared storage means two processes must never write the same persistence file. Adds the whole class of split-brain problems the valkey operator spent ADRs 0008, 0009, 0011 and 0025 on. | A later step, if A proves too slow. |

**Recommendation:** **A**, and say so in the README in one sentence — *this operator provisions a
single-active broker with bounded failover, not a cluster.* Then make `spec.replicas > 1` mean
option B explicitly and gate it behind a field that names what the user is giving up
(see HA2). D is the flip pair below and
should not shape the API before this research is reopened.

The reason to prefer A is not simplicity. It is that A is the only option where the operator can
state a guarantee it can actually test. B and C both have failure modes that only appear under a
client mix we do not control.

**Answer:** _open_

### HA2 (P0) — What does `spec.replicas > 1` mean after HA1?

**What it blocks:** CRD validation, the anti-affinity story, and whether we have to remove a field
that already shipped in `v0.1.0`.

Three ways out, and the third is the trap:

1. **Cap it at 1** and delete the field's current meaning. Honest, and a breaking API change on a
   version nobody runs — which is exactly when a breaking change is free.
2. **Keep it, rename its meaning.** `replicas` stays, but the docs and the CRD description say
   "independent brokers, no shared state", and a `status` condition names it. Needs an explicit
   opt-in field so nobody reaches option B by accident.
3. **Keep it and stay quiet.** What we have now. A user reads `replicas: 3` as redundancy, gets
   three brokers that disagree, and finds out under load.

**Recommendation:** 2, with the opt-in. Something like `spec.topology: single | independent`,
defaulting to `single`, where `independent` is the only value that accepts `replicas > 1`. The
field name carries the warning into `kubectl explain`, which is where people actually look.

**Answer:** _open_

### HA3 (P1) — Is bounded failover good enough, and what is the bound?

**What it blocks:** whether the flip pair (option D) is ever built, the PDB defaults, the probe
defaults, and whether we need RWX storage anywhere.

Ask it as a number, not as a preference: **what is the longest MQTT outage the worst client on
the fleet tolerates?** A gateway with a 30-second reconnect backoff and QoS 1 does not care about
a 20-second gap. A control loop that treats a missed message as a fault does.

If the answer is "seconds", A is fine and the flip pair is never built. If it is "sub-second", no
open-source Mosquitto topology delivers it and the honest answer is a different broker or the
commercial edition — which should then be written down rather than discovered in month four.

**Answer:** _open_

### HA4 (P1) — Persistence backend: classic or `persist-sqlite`?

Mosquitto 2.1 ships `mosquitto_persist_sqlite.so` (verified present in the pinned image). The two
cannot both be on, and the broker refuses to start rather than picking one — measured, exact
message:

```
Error: `persistence true` cannot be used with a persistence plugin.
Sqlite persistence: Unable to register plugin (3)
```

Which matters beyond the choice itself: the generated config currently emits `persistence true`
unconditionally, so switching backends is a change to the generator, not a value a user can set
through `spec.config`.

| | Classic `persistence true` (today) | `persist-sqlite` plugin |
|---|---|---|
| Write pattern | Periodic full dump of the in-memory DB | Transactional, `plugin_opt_sync` = `extra`/`full`/`normal`/`off` |
| Crash loss window | Up to `autosave_interval` | Bounded by the sync mode |
| Maturity | Since forever | New in 2.1 |
| Removed in 3.0? | Not announced | It is the forward path |

**Recommendation:** stay on classic until this is reopened, and make the backend a field so switching later
is a config change and not an API change. The crash-loss window matters exactly as much as HA3's
answer says it does — decide it there, not here.

**Answer:** _open_

### HA5 (P0) — What does "version update without interrupting operation" mean here?

**It has to be reworded before it can be built, and the rewording is the answer.**

With a single active broker (HA1 option A), a version change means the process stops and a new one
starts. A StatefulSet has no surge — `maxSurge` is a Deployment concept — so there is no overlap
window in which both versions serve. **Zero interruption is not reachable.** Anyone who says
otherwise is describing a different broker.

What *is* reachable, and worth committing to:

- **No message loss.** QoS 1/2 with persistent sessions and persistence on a PVC: the session and
  its queued messages survive the restart, and the client gets them on reconnect.
- **A bounded gap.** Pre-pulled image, tuned `terminationGracePeriodSeconds`, readiness that
  reflects the listener rather than the process. Order of seconds, and measurable in E2E.
- **A predictable one.** The roll happens when the operator decides, not when the kubelet does.

Three things that would make even that false, and each is a question of its own: clients on QoS 0,
clients using clean sessions, and clients with no reconnect backoff. Those lose messages across
any restart, and no operator can fix it from this side.

**Recommendation:** state the goal as **"no message loss and a bounded, measured reconnect gap"**,
put the number in the README, and hold it with an E2E test that publishes at QoS 1 across a
version change and asserts nothing was dropped. Drop the phrase "without interruption" from every
document, because it promises something the broker cannot do.

**Answer:** _open_

### HA6 (P1) — Fencing for a flip-based failover: may the operator kill a broker pod?

*Only reachable if HA1 ever activates the flip pair (option D).*

**Security and authority question.** A Service-selector flip moves *new* connections to the
standby. Clients already connected to the sick primary stay on it — a broker the Service no
longer names, possibly on the wrong side of a partition, converging retained topics
last-writer-wins after the heal. The clean fix is fencing: kill the old pod.

But the ClusterRole deliberately holds **no `delete` on anything**
([ADR 0009](../adr/0009-delete-only-through-owner-references.md)) — teardown belongs to the
garbage collector, and that absence is a real security property (a compromised operator cannot
destroy workloads). Fencing-by-delete would be the first deliberate breach of that rule.

| Option | Cost |
|---|---|
| Accept and document: no fencing, bridge keeps the halves converging | Stale primary serves its clients indefinitely; retained divergence during partitions. |
| `delete` on `pods`, scoped as narrowly as RBAC allows | ADR 0009 amendment; verbs on `pods` are cluster-wide in a ClusterRole. Parity test and both install paths move together ([ADR 0006](../adr/0006-both-install-paths-grant-the-same-authority.md)). |
| In-band fence: operator (or a sidecar) tells the old broker to stop accepting/close listeners | No new RBAC, but needs a control channel into the pod — the same class of machinery the reload sidecar of [ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md) already adds. |

**Recommendation:** defer with the pair itself; when the flip pair is built, prefer the in-band fence,
and treat any `delete` grant as an ADR-0009 amendment with its own adversarial review.

**Answer:** _open_

### HA7 (P1) — Is a PodDisruptionBudget in scope, and is it opt-in?

None is shipped. The argument is sharper here than for a replicated store: with a single active
broker, a PDB with `maxUnavailable: 0` blocks node drains entirely, and one with
`maxUnavailable: 1` permits exactly the disruption the design tries to bound. A PDB on a
one-replica StatefulSet is close to a statement about who is allowed to cause the outage, not
about avoiding it.

**Recommendation:** opt-in, and designed only once HA3 has a number to design it against.

**Answer:** _open_

## Sources

Read 2026-09-01.

- Raymii — [High Available Mosquitto MQTT on Kubernetes](https://raymii.org/s/tutorials/High_Available_Mosquitto_MQTT_Broker_on_Kubernetes.html)
- Cedalo — [Mosquitto MQTT with HA in Kubernetes](https://www.cedalo.com/blog/mosquitto-mqtt-on-kubernetes), [Pro Mosquitto High Availability](https://www.cedalo.com/pro-mosquitto/high-availability)
- EMQX — [Mria cluster architecture](https://docs.emqx.com/en/emqx/latest/deploy/cluster/mria-introduction.html), [Clustering design](https://docs.emqx.com/en/emqx/latest/design/clustering.html), [100M connections](https://www.emqx.com/en/blog/how-emqx-5-0-achieves-100-million-mqtt-connections), [Sticky sessions / HAProxy MQTT](https://www.emqx.com/en/blog/mqtt-broker-clustering-part-2-sticky-session-load-balancing), [Operator blue-green upgrade](https://docs.emqx.com/en/emqx-operator/latest/tasks/configure-emqx-blueGreenUpdate.html), [Node evacuation and rebalancing](https://docs.emqx.com/en/emqx/latest/deploy/cluster/rebalancing.html)
- VerneMQ — [Clustering introduction](https://docs.vernemq.com/master/vernemq-clustering/introduction), [Netsplits](https://docs.vernemq.com/master/vernemq-clustering/netsplits)
- NATS — [MQTT support](https://docs.nats.io/running-a-nats-service/configuration/mqtt), [README-MQTT](https://github.com/nats-io/nats-server/blob/main/server/README-MQTT.md), [JetStream clustering](https://docs.nats.io/running-a-nats-service/configuration/clustering/jetstream_clustering)
- TBMQ — [Architecture](https://thingsboard.io/docs/mqtt-broker/architecture/), [Repository](https://github.com/thingsboard/tbmq), [MDPI paper on TBMQ scalability](https://www.mdpi.com/2624-831X/6/3/34)
- Mosquitto — [mosquitto.conf man page (bridges: `try_private`, `bridge_outgoing_retain`, loops)](https://mosquitto.org/man/mosquitto-conf-5.html), [SQLite persistence](https://mosquitto.org/documentation/persistence/sqlite/), [2.1.0 release](https://mosquitto.org/blog/2026/01/version-2-1-0-released/)
- Dead fork — [hui6075/mosquitto-cluster](https://github.com/hui6075/mosquitto-cluster) (last push 2019-02-14 per GitHub API)
- Litestream — [How it works](https://litestream.io/how-it-works/), [Tips & caveats](https://litestream.io/tips/), [WAL-reuse replication failure #1083](https://github.com/benbjohnson/litestream/issues/1083)
- Kubernetes — [Pod with volume cannot fail over when node goes down #112016](https://github.com/kubernetes/kubernetes/issues/112016)
