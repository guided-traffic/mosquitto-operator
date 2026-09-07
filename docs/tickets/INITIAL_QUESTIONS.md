# Initial question catalogue

Opened: 2026-09-01. Status: **unanswered by design** — this file exists to be decided before
implementation starts, not alongside it.

Companion: [`INITITAL_PLAN.md`](INITITAL_PLAN.md) carries the plan and the measurements the
questions below rest on. Read the measurements first: several questions that look open are
already narrowed by what the pinned image actually does.

Each question states what it blocks, the options with their real cost, and a recommendation.
A recommendation is a starting position, not a decision — the answer column is empty on purpose.

---

## How to answer

Answer by ID, in this file, under the `**Answer:**` line of each question. An answer that
changes an option's cost rather than picking one is a valid answer and should say so. When a
question is answered, the ADR it feeds is listed in [`INITITAL_PLAN.md`](INITITAL_PLAN.md) and
gets written in the same change as the code.

Priority marks:

| Mark | Meaning |
|---|---|
| **P0** | Blocks the next phase. Nothing downstream can be designed without it. |
| **P1** | Blocks a specific phase, not the next one. |
| **P2** | Can be deferred without rework, but the answer is cheaper to get now. |

---

# A. High availability — what are we actually promising

The honest starting point, measured and unambiguous: **open-source Mosquitto has no clustering.**
No shared sessions, no shared retained messages, no state replication, no leader election. A
second broker process is a second broker, not a second copy of the first. Everything in this
section is about what we build *on top of* that, and what we then write in the README.

Today the operator already produces `replicas: 3` behind one ClusterIP Service. That shape is
correct for exactly one workload class and wrong for every other, which is why this section is
first.

### Q1 (P0) — Which HA shape is the product?

**What it blocks:** the meaning of `spec.replicas`, whether the operator does leader election,
the Service topology, the PVC layout, the whole storage story, and the first sentence of the
README.

| Option | What it gives | What it costs | Correct for |
|---|---|---|---|
| **A — Single active, fast failover.** `replicas: 1`, PVC, tuned probes, PDB, priorityClass, pre-pulled image. | Full MQTT semantics. Sessions and retained messages survive a pod move because they are on the PVC. One authority, no split brain. | A reschedule is a gap: RWO PVC detach plus scheduling plus broker start. Order of seconds to a minute, measurable, never zero. | Everything stateful: QoS 1/2, persistent sessions, retained messages. |
| **B — N independent brokers, one Service.** Today's shape. | Horizontal connection capacity. Survives one pod dying without a gap for *new* connections. | Not one broker. A retained message published through pod 0 is invisible on pod 1. A persistent session is stranded on whichever pod took it. A subscriber and a publisher that land on different pods never see each other. | QoS 0 fan-out with no retained messages and no persistent sessions — and only if every client accepts that. |
| **C — Bridge mesh.** N brokers, each bridging to the others. | Publishes propagate between brokers, so subscribers on different pods do see each other. | Retained and session state stay local anyway. Loop and duplicate risk. `bridge` reconnect storms. N*(N-1) connections. Every topic that must cross needs a topic-map entry. QoS across a bridge is per-hop, not end to end. | Fan-out across zones where duplicates are acceptable. |
| **D — Operator-elected primary.** N pods, only the elected one is in the Service selector; the rest are warm standbys. | Failover without waiting for scheduling. The operator controls the switch and can record it. | The standby holds no state unless storage is shared, and shared storage means two processes must never write the same persistence file. Adds the whole class of split-brain problems the valkey operator spent ADRs 0008, 0009, 0011 and 0025 on. | A later phase, if A proves too slow. |

**Recommendation:** **A**, and say so in the README in one sentence — *this operator provisions a
single-active broker with bounded failover, not a cluster.* Then make `spec.replicas > 1` mean
option B explicitly and gate it behind a field that names what the user is giving up
(see [Q2](#q2-p0--what-does-specreplicas--1-mean-after-q1)). D is a phase-5 conversation and
should not shape the API now.

The reason to prefer A is not simplicity. It is that A is the only option where the operator can
state a guarantee it can actually test. B and C both have failure modes that only appear under a
client mix we do not control.

*Added 2026-09-01:* [`HA_RESEARCH.md`](HA_RESEARCH.md) surveys how the field does MQTT HA and
measures a concrete form of option D — the bridged flip pair (retained state replication
measured, session loss measured, node-loss failover in seconds instead of the ~6-minute RWO
bound). It does not change this recommendation for the first release; it makes D real for
phase 5 and adds Q22.

**Answer:**

### Q2 (P0) — What does `spec.replicas > 1` mean after Q1?

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

**Answer:**

### Q3 (P1) — Is bounded failover good enough, and what is the bound?

**What it blocks:** whether phase 5 (option D) is ever built, the PDB defaults, the probe
defaults, and whether we need RWX storage anywhere.

Ask it as a number, not as a preference: **what is the longest MQTT outage the worst client on
the fleet tolerates?** A gateway with a 30-second reconnect backoff and QoS 1 does not care about
a 20-second gap. A control loop that treats a missed message as a fault does.

If the answer is "seconds", A is fine and phase 5 never happens. If it is "sub-second", no
open-source Mosquitto topology delivers it and the honest answer is a different broker or the
commercial edition — which should then be written down rather than discovered in month four.

**Answer:**

### Q4 (P1) — Persistence backend: classic or `persist-sqlite`?

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

**Recommendation:** stay on classic for phase 1, and make the backend a field so switching later
is a config change and not an API change. The crash-loss window matters exactly as much as Q3's
answer says it does — decide it there, not here.

**Answer:**

### Q22 (P1) — Fencing for a flip-based failover: may the operator kill a broker pod?

*Added 2026-09-01 from [`HA_RESEARCH.md`](HA_RESEARCH.md). Only reachable if Q1 ever activates
the flip pair (option D / phase 5).*

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
| In-band fence: operator (or a sidecar) tells the old broker to stop accepting/close listeners | No new RBAC, but needs a control channel into the pod — the same class of machinery Q13's sidecar already adds. |

**Recommendation:** defer with the pair itself; when phase 5 opens, prefer the in-band fence,
and treat any `delete` grant as an ADR-0009 amendment with its own adversarial review.

**Answer:**

---

# B. Users and ACLs — the data model

This is where the plan deliberately diverges from the valkey operator. Stated as a fact rather
than a preference: **`valkey-operator` has no user model at all.** Its `AuthSpec` is two fields —
a Secret name and a key — because Valkey in that deployment has one password and no per-client
authorisation. There is nothing to copy. The question is not "inline like valkey or separate",
it is "what shape does a thing valkey never had take".

### Q5 (P0) — Separate CRs for users, or inline in the `Mosquitto` spec?

**What it blocks:** every controller, every RBAC rule, the entire phase-2 design.

The case for separate objects — and it is strong enough that this question is really about
confirming it:

- **Lifecycle.** A broker is provisioned once. Clients arrive and leave continuously, often from
  a different pipeline than the one that created the broker. Two lifecycles in one object means
  every client change rewrites the broker object.
- **Write conflicts.** Inline means every user addition is a read-modify-write of one resource.
  Two pipelines adding a client at the same time is a conflict-and-retry loop that gets worse
  linearly with fleet size.
- **Object size.** One CR is bounded by the etcd value limit. Several hundred clients with ACL
  lists is not a hypothetical ceiling for MQTT.
- **RBAC.** This is the decisive one. Today, edit on a `Mosquitto` is total control of the broker,
  because `spec.config` is appended verbatim and unvalidated. If users are inline, granting a team
  the right to add their own client grants them the right to rewrite the broker configuration.
  A separate kind is the only way to delegate one without the other.
- **Status.** A per-user object can carry its own conditions: reconciled, rejected, password
  Secret missing. Inline, all of that has to be squeezed into a list in the parent status.

The cost of separate objects, stated fairly: two more controllers, an index from broker to users,
a rendering step that has to be deterministic (see [Q9](#q9-p1--how-is-the-rendered-auth-material-made-deterministic)),
and a cross-namespace binding question ([Q8](#q8-p0--may-a-mosquittouser-bind-to-a-broker-in-another-namespace)).

**Recommendation:** separate. Confirm it and move on.

**Answer:**

### Q6 (P0) — Which kinds, exactly?

**What it blocks:** the CRD count, and how much of it we can defer.

| Option | Kinds | Note |
|---|---|---|
| **Minimal** | `MosquittoUser` with an inline `acls` list | One controller. No sharing: 200 clients with the same permissions means 200 copies of the same ACL list. |
| **Roles** | `MosquittoUser` + `MosquittoRole` | A user references roles. Matches the dynsec model one-to-one, so it survives a later switch to dynsec. Two controllers. |
| **Roles + groups** | `+ MosquittoGroup` | Full dynsec parity. Groups only earn their place if roles alone force duplication in practice. |

**Recommendation:** **Roles**, with the inline `acls` list kept on the user as well for the
one-off case. Groups are a `+1` later and cost nothing to add if the role indirection exists.

The reason to build roles now rather than later: it is the same object graph dynsec uses. If
[Q11](#q11-p0--password-file--acl-file-or-dynamic-security) is ever re-decided toward dynsec, a
role-shaped API maps across; a flat one does not.

**Answer:**

### Q7 (P1) — Which direction does the reference point?

**What it blocks:** the watch and index setup, and whether adding a user needs write access to
the broker object.

- **User → broker** (`spec.brokerRef`), like `Ingress` naming its class. Adding a client touches
  nothing the broker owns. Requires the broker controller to list users by index and to be woken
  by a user change.
- **Broker → user** (a selector on the `Mosquitto`), like a `Service` selecting pods. Broker keeps
  authority over who may attach. Requires editing the broker to onboard a client, which reopens
  the RBAC problem from Q5.

**Recommendation:** user → broker, with the broker holding an *acceptance policy*, not a list —
see Q8. That keeps the delegation working while leaving the broker owner in control of the rule.

**Answer:**

### Q8 (P0) — May a `MosquittoUser` bind to a broker in another namespace?

**Security question. It needs an explicit decision, not a default.**

If a `MosquittoUser` in namespace `team-a` may reference a `Mosquitto` in namespace `messaging`,
then anyone who can create that CR in their own namespace can mint a credential on a broker they
do not own, with whatever ACLs they wrote. That is a privilege escalation across a namespace
boundary, and namespaces are the tenancy boundary in almost every cluster.

The precedent worth copying is Gateway API: the *target* declares who may attach to it
(`allowedRoutes`), the source never decides on its own.

| Option | Risk |
|---|---|
| **Same namespace only** | None new. Restrictive: every client team needs a broker in their own namespace, or must be able to write into the broker's. |
| **Broker opts in with a namespace selector** | Bounded. The broker owner names which namespaces may attach; a user from anywhere else is rejected with a status condition. |
| **Any namespace** | A cluster-wide escalation path. Not acceptable without a specific reason. |

Second half of the same question: **may a user grant itself ACLs the broker owner would refuse?**
Even same-namespace, if the ACL list is arbitrary, a user object can subscribe to `#`. Does the
broker need a policy — a topic prefix per namespace, a maximum scope, a deny on `$SYS` and
`$CONTROL`?

**Recommendation:** opt-in selector on the broker, default **same namespace only**, plus a
mandatory deny on `$CONTROL/#` and `$SYS/#` for every rendered user regardless of what the CR
asks for. The second half — a topic-prefix policy — is worth its own field but can land in
phase 3.

**Answer:**

### Q9 (P1) — How is the rendered auth material made deterministic?

**What it blocks:** whether a no-op reconcile can produce a broker reload.

N user objects render into one password file and one ACL file. Map iteration order in Go is
random; a set of users read from a list-watch has no inherent order. If the render is not sorted
and stable, every reconcile produces a different byte sequence, the hash changes, and the broker
gets a SIGHUP it did not need — forever.

**Recommendation:** sort by `(namespace, name)`, render through a single function, and put a test
on it that renders the same input a hundred times and asserts one distinct output. Cheap, and the
class of bug it prevents is one that only shows up as unexplained load.

**Answer:**

### Q10 (P2) — What happens to connected clients when their user is deleted?

Mosquitto does not disconnect a client because the password file stopped listing it. The
credential is checked at CONNECT. An existing connection survives a reload.

| Option | Effect |
|---|---|
| Do nothing | The client keeps its session until it disconnects. Could be days. A revoked credential is not revoked. |
| Kick on delete | Needs `mosquitto_ctrl broker` or dynsec's `kick` — that is, a live admin connection from the operator. |
| Roll the broker | Correct and brutal: every client on that broker reconnects. |

**Recommendation:** do nothing in phase 2, **document it as a residual risk in the ADR**, and
report it on the user's status as a condition (`Revoked`, message: existing connections are not
terminated). Revocation latency is a real security property and it must not be silently absent.
If Q3's answer is "revocation must be immediate", that flips [Q11](#q11-p0--password-file--acl-file-or-dynamic-security)
toward dynsec on its own.

**Answer:**

---

# C. The authentication mechanism

Everything here rests on measurements taken on 2026-09-01 against `eclipse-mosquitto:2.1.2-alpine`.
The full record is in [`INITITAL_PLAN.md`](INITITAL_PLAN.md) section "Measured ground". The short
version: **the file-based plugins reload on SIGHUP and the dynamic-security plugin does not.**

### Q11 (P0) — `password-file` + `acl-file`, or `dynamic-security`?

**What it blocks:** phase 2 entirely. Everything else in section B is shape; this is mechanism.

| | `password-file` + `acl-file` | `dynamic-security` |
|---|---|---|
| Runtime change without restart | **Yes — SIGHUP, measured** | **Yes — over `$CONTROL/dynamic-security/v1`, measured** |
| Reload from a file the operator wrote | **Yes** | **No — SIGHUP does not re-read the JSON, measured** |
| Who owns the file | The operator, exclusively | The plugin. It rewrites the file on every change |
| Operator needs an MQTT connection to the broker | No | Yes, as an admin client with a credential |
| Reconciliation model | Declarative: render, compare, write | Imperative: diff current state against desired, issue commands |
| Groups, roles, ACL priorities | Roles by expansion; no priorities | Native |
| Disconnect a revoked client | No | Yes (`kick`) |
| Deprecation risk | The *options* `password_file`/`acl_file` go in 3.0; the **plugins are the replacement** | None |
| Storage | Read-only mount is enough | Needs a writable volume the operator does not control |

The decisive asymmetry is the row about who owns the file. With dynsec, the desired state lives in
CRs and the actual state lives in a JSON file that the *broker* writes, on a volume, per pod. Two
writers, no transaction, and reconciliation becomes a bidirectional sync against a live MQTT
connection — plus the operator now needs a credential, a network path to every broker pod, and a
retry story for when the broker is down. With the file plugins the operator is the only writer and
the reconcile is a render-and-compare, which is what the rest of this operator already is.

The honest cost of the file plugins: no `kick` (see Q10), no ACL priorities, and roles must be
expanded at render time rather than referenced.

**Recommendation:** **file plugins.** Keep the API role-shaped (Q6) so dynsec stays reachable, and
write the ADR so the re-decision has a named trigger — if Q10 comes back as "revocation must be
immediate", or if ACL priorities turn out to be needed, that is the trigger.

**Answer:**

### Q12 (P0) — Where do passwords come from, and who hashes them?

**Security question.**

The password file holds hashes, not passwords. `mosquitto_passwd` writes `$7$` (PBKDF2-HMAC-SHA512)
and 2.1 also understands `$argon2id$`. The operator image is distroless and contains no
`mosquitto_passwd`.

| Option | How it works | Cost / risk |
|---|---|---|
| **A — Operator hashes in Go** | User references a Secret with a plaintext password; the operator reads it, derives `$7$`, writes the file. | Operator needs `get` on `secrets` — a rule the ClusterRole deliberately does not have today, and adding it makes the operator able to read every Secret in the cluster. Must reimplement the exact `$7$` format. |
| **B — User supplies the hash** | The CR or its Secret carries `$7$...` already. Operator never sees a plaintext password. | Onboarding friction: someone has to run `mosquitto_passwd`. Nothing validates the hash until the broker rejects it. |
| **C — Init/sidecar container from the broker image** | The mosquitto image already has `mosquitto_passwd`. A helper container renders the file inside the pod. | Plaintext still has to reach that container. Moves the secret exposure from the operator into the broker pod, which is arguably the right place — it is the only thing that needs it. |
| **D — Operator generates the password** | Operator mints it, writes the hash to the file and the plaintext to a Secret the client reads. | Same `secrets` grant as A, plus `create`. Best onboarding UX. |

The grant in A and D is the thing to weigh. `get` on `secrets` cluster-wide turns a compromise of
the operator from "can rewrite broker configs" into "can read every credential in the cluster".
The current ClusterRole has no `secrets` rule at all, and that is a property worth spending
something to keep.

**Recommendation:** **B for phase 2**, C or D behind an explicit later decision. B needs no new
RBAC, no new hash implementation and no new secret path. If onboarding friction turns out to be
the blocker, D with a **namespace-scoped** grant (a Role per watched namespace, not a ClusterRole)
is the next step — and that is an ADR, not a patch.

**Answer:**

### Q13 (P0) — How does the material reach the broker, given that Kubernetes cannot write a file Mosquitto will accept?

**Measured, and it constrains the design hard.** Mosquitto 2.1 already warns on the files a
Kubernetes volume produces, and says future versions will refuse them:

| File state | Broker output |
|---|---|
| Secret volume default — `root:1883`, mode `0644` | `Warning: File ... has world readable permissions. Future versions will refuse to load this file.` **and** `Warning: File ... owner is not mosquitto.` |
| `defaultMode: 0640` plus `fsGroup: 1883` | `Warning: File ... owner is not mosquitto. Future versions will refuse to load this file.` |
| `mosquitto:mosquitto`, mode `0600` | Clean |

Kubernetes writes Secret and ConfigMap volume files owned by **root**. `fsGroup` sets the group,
never the owner. So **no Secret or ConfigMap mount can ever reach the clean row.** It works today
on 2.1 with a warning, and it is on a stated path to breaking.

| Option | Mechanism | Note |
|---|---|---|
| **A — Mount the Secret directly** | `plugin_opt_password_file` points into the Secret mount | Works now, two warnings, breaks on a future minor. Reload on SIGHUP still needs a signaller. |
| **B — initContainer copies to an emptyDir** | Runs as 1883, `cp` + `chmod 600`, broker reads the copy | Clean file. But an initContainer runs once: a rotated Secret never reaches the copy. Pairs only with a restart-based update. |
| **C — Sidecar copies and signals** | Watches the projected Secret, copies on change, sends SIGHUP | Clean file **and** live reload. Needs `shareProcessNamespace: true` so the sidecar can signal the broker. This is the valkey `internal/tlsmaterial` reloader pattern, one layer down. |
| **D — Config hash, roll the pod** | What the operator does for `mosquitto.conf` today | Simple, no new container. Every user change disconnects every client on that broker. |

**Recommendation:** **C.** It is the only option that is both correct on file ownership and
non-disruptive on change, and the operator already has the sibling problem solved in the valkey
codebase to copy from. D is the phase-2 fallback if C slips — it is correct, just loud.

Note what C costs: `shareProcessNamespace: true` means every container in the pod can see and
signal every other one, and the process namespace no longer isolates the broker from its
sidecars. In a pod whose containers we all build, that is acceptable; it should be written down
rather than assumed.

**Answer:**

### Q14 (P1) — Should `spec.config` stay unvalidated?

**Security and correctness question.** Today `spec.config` is appended verbatim and nothing checks
it. A bad line is a CrashLoopBackOff, not a rejected resource. Worse, a `listener` line there adds
a listener the operator does not model — and after phase 2, a line there can *undo* the
authentication the user objects configured, because a later directive wins.

Measured: `mosquitto --test-config` exists in the pinned image, exits `0` on a good file and `3`
on a bad one, and names the file and line:

```
Error: Unknown configuration variable 'this_is_garbage'.
Error found at /tmp/bad.conf:2.
```

Also measured, and important: **`--test-config` does not validate plugin options.** A config with
a wrong `plugin_opt_*` key passed `--test-config` cleanly and only failed at runtime with
`password-file: Error: Unknown option 'file'.` So it is a syntax gate, not a correctness gate.

| Option | Effect |
|---|---|
| Leave it | A typo is a CrashLoop. After phase 2, a directive can silently disable auth. |
| Validating webhook running `--test-config` | Rejects bad syntax at `kubectl apply`. Needs a webhook, which needs a serving certificate — and this project ships no cert-manager dependency ([ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md)). |
| initContainer runs `--test-config` | No webhook, no certificate. Failure is still a non-starting pod, but with a readable reason instead of a crash loop. |
| Deny-list a few directives at render time | Catches the auth-undo case specifically. Cheap. Incomplete by construction. |

**Recommendation:** initContainer plus a small deny-list on the directives that can disable
authentication once phase 2 exists. The webhook is a real answer but it drags a certificate
requirement into a project that deliberately has none.

**Answer:**

---

# D. Upgrades and operation

### Q15 (P0) — What does "version update without interrupting operation" mean here?

**It has to be reworded before it can be built, and the rewording is the answer.**

With a single active broker (Q1 option A), a version change means the process stops and a new one
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

**Answer:**

### Q16 (P1) — Does the operator manage broker versions, or only accept them?

Today `spec.image` is a free string and Renovate moves the default. Options: keep it, add a
`spec.version` with an operator-owned image map, or add an upgrade policy (allowed jumps,
maintenance windows).

Relevant constraint: Mosquitto 2.1 deprecates `per_listener_settings`, `acl_file` and
`password_file`, and 3.0 removes them. A generated config that is valid on 2.1 can be invalid on
3.0. If the operator generates config, the operator owns that compatibility.

**Recommendation:** keep the free image field, and add a **config generation targeted at a
version floor** — generate only what 2.1 accepts and 3.0 keeps, which is what
[ADR 0007](../adr/0007-one-broker-image-pin-and-why-not-the-openssl-tag.md) already reasons
toward. Then an image bump is a user decision and the generated file is version-safe by
construction. Revisit if a 3.0 pin becomes real.

**Answer:**

### Q17 (P1) — Are PodDisruptionBudget and NetworkPolicy in scope, and are they opt-in?

Neither is shipped today. valkey has an opt-in PDB
(its ADR 0004, in the `valkey-operator` repository) and a
NetworkPolicy spec.

The PDB argument is sharper here than in valkey: with a single active broker, a PDB with
`maxUnavailable: 0` blocks node drains entirely, and one with `maxUnavailable: 1` permits exactly
the disruption we are trying to bound. A PDB on a one-replica StatefulSet is close to a
statement about who is allowed to cause the outage, not about avoiding it.

The NetworkPolicy argument is that the broker is anonymous today
([ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)),
so the only thing bounding exposure is that nobody routes to the ClusterIP. That is not a control.

**Recommendation:** NetworkPolicy in phase 3 alongside authentication — the two answer the same
exposure from different directions, and shipping auth without a way to bound reachability leaves
the weaker half of the story in place. PDB opt-in, phase 4, once Q3 has a number to design it
against.

**Answer:**

---

# E. Observability

### Q18 (P2) — Does the exporter of [ADR 0002](../adr/0002-the-metrics-exporter-is-written-here.md) stay decided as written?

The decision stands and nothing is implemented: `cmd/exporter` in this repo, a `$SYS` subscription
via `paho.mqtt.golang`, sidecar per broker pod, second binary in the same image, no credential of
its own, no readiness probe.

The one thing phase 2 changes: D4 says the exporter reuses "whatever principal the API brings"
once authentication is modelled. After Q11 that is concrete — the exporter needs a user, and
that user needs `subscribePattern` on `$SYS/#`. Which means either the operator mints a system
user per broker, or `$SYS` access needs a carve-out.

**Recommendation:** the operator renders a reserved user for the exporter, named so it cannot
collide with a `MosquittoUser` (see Q19), with exactly one ACL: read on `$SYS/#`. Decide it in
phase 2's ADR, build it in phase 6.

**Answer:**

### Q19 (P1) — What is the reserved namespace for operator-generated principals?

Once the operator generates users, a user-created `MosquittoUser` could claim the same username as
an operator-internal one and take over its ACLs. Same class of problem the operator already solves
for Kubernetes objects with `ensureOwned`, one layer down: **the username space needs a reserved
prefix, and rendering must refuse a CR that asks for it.**

**Recommendation:** reserve a prefix, reject it in CRD validation *and* at render time — validation
is a shape check and render is the authority, exactly the split
[ADR 0009](../adr/0009-delete-only-through-owner-references.md) already draws for object writes.

**Answer:**

---

# F. Delivery

### Q20 (P1) — What is the API version story?

`v1` shipped in `v0.1.0`. Phase 2 adds kinds and probably changes `spec.replicas` semantics (Q2).

The project is pre-alpha with no users and no deployment — recorded, deliberately, as the reason
it stays on `0.x`. A breaking change costs nothing **now** and gets permanently more expensive
with every week it waits.

**Recommendation:** make all of the shape changes in `v1` while it is free, in one change, and put
a line in the README stating that `v1` is unstable until `v1.0.0` of the operator. Do not
introduce `v1alpha1` — a conversion webhook needs a serving certificate, and that is the
dependency this project does not take (Q14, ADR 0001).

**Answer:**

### Q21 (P2) — Do the new kinds ship in the same chart and the same image?

Both install paths must grant the same authority
([ADR 0006](../adr/0006-both-install-paths-grant-the-same-authority.md)) and the parity test
compares what they render. New CRDs mean new RBAC on both sides, and the chart's ClusterRole is
hand-written.

**Recommendation:** same chart, same image, and let `make verify-rbac-parity` be the thing that
catches the hand-written half drifting — it already does exactly this and has been observed
failing on purpose ([ADR 0010](../adr/0010-a-check-is-not-a-check-until-it-has-failed-on-purpose.md)).

**Answer:**

---

## Dependency order

Answering out of order wastes work. The chain:

```
Q1 (HA shape)
 ├─> Q2 (replicas semantics) ─> Q20 (API version story)
 ├─> Q3 (failover bound) ──┬─> Q4 (persistence backend)
 │                         └─> Q17 (PDB)
 ├─> Q15 (upgrade promise)
 └─> Q22 (flip-pair fencing)   <- security gate, phase 5 only

Q5 (separate CRs)
 ├─> Q6 (which kinds) ─> Q19 (reserved names) ─> Q18 (exporter principal)
 ├─> Q7 (reference direction) ─> Q8 (cross-namespace)   <- security gate
 └─> Q11 (mechanism)  <- also gated by Q10 (revocation latency)
      ├─> Q12 (password origin)   <- security gate
      ├─> Q13 (material delivery) <- constrained by measurement, not preference
      ├─> Q9  (deterministic render)
      └─> Q14 (spec.config validation)
```

**The four that unblock the most: Q1, Q5, Q11, Q12.** Q8 and Q12 are the two where the wrong
answer is a security property we would have to take back later, so they should not be answered
quickly.
