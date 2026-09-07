# HA research: how the field makes MQTT highly available, and which move is open to us

Opened: 2026-09-01. Method: web survey of broker architectures, operators and deployment
patterns, plus local measurements against the pinned image (`eclipse-mosquitto:2.1.2-alpine`,
docker 28.4.0, arm64 — same rig as the measurements in
[`INITITAL_PLAN.md`](INITITAL_PLAN.md) section 2, continuing their numbering at M10).

Feeds: [Q1, Q3, Q15](INITIAL_QUESTIONS.md) and phase 5 of the plan. Sources at the end;
every claim not marked **measured** is a claim from a source, not from us.

---

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

## 3. Measured locally (continuing the plan's M-numbering)

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
  of [Q18/Q19](INITIAL_QUESTIONS.md), which this design would reuse rather than invent).

What it delivers, measured or bounded, against the plan's option A (single pod + PVC):

| Property | Single + PVC (plan option A) | Flip pair |
|---|---|---|
| Pod crash | seconds (restart, same PVC) | seconds (flip) |
| **Node loss** | **~6 min** (RWO force-detach) | **detection + ~1 s flip** — no volume moves |
| Retained messages | survive (PVC) | survive (bridge, **measured M10**) |
| Persistent sessions + queued QoS | **survive** (PVC) | **lost on flip (measured M11)** |
| Split brain | impossible (one broker) | possible during partition; see below |
| Cost | 1× | 2× resources, bridge config, flip logic, fencing |

Neither dominates. They are two different promises, which is why the CRD field should be a
mode (`spec.ha.mode: none | standby`, working name), not a replica count — and it slots into
Q1/Q2 exactly where `topology` was proposed.

**The elegant part — the flip is also the upgrade.** Bring the standby up on the new image,
let the bridge carry the retained state across, flip, keep the old primary as the new standby.
The reconnect gap replaces the restart gap of Q15, and rollback is flipping back. This is
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
   fencing-by-delete is an authority extension that needs its own decision. → **Q22**.
2. **Bridge hygiene.** Bridge patterns must exclude `$SYS/#` and `$CONTROL/#`; post-phase-2
   the bridge needs a credential — a second reserved principal (Q19).
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
  MQTT-aware LB in front (HAProxy ≥ 2.4). A refinement of the plan's option B for the
  semantics-limited workload class; parked until someone owns that workload.
- **Being honest about the ceiling:** if the requirement is session takeover — the M11 line —
  the answer is a broker from model M-A/M-B (EMQX, VerneMQ, NATS, TBMQ) or the commercial
  Mosquitto, and this operator's README should say exactly that sentence rather than imply the
  pair is a cluster.

## 6. Effect on the open questions

- **Q1** — option D graduates from sketch to the measured flip-pair above. Recommendation
  unchanged for the first release (A), with D as the concrete phase-5 shape behind
  `spec.ha.mode`.
- **Q3** — the bound now has numbers to choose between: ~200 ms (commercial Raft), ~1–5 s
  (flip pair, detection-bound), seconds-to-minutes (single + PVC, depending on failure class).
- **Q15** — gains the flip-upgrade; the promise stays "no message loss for sessions that stay
  on their broker, bounded gap", and the M11 caveat goes into the README verbatim.
- **New Q22** (added to the catalogue) — fencing authority for the flip pair vs. ADR 0009's
  no-delete rule.

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
