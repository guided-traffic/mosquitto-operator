# Initial plan: from a broker launcher to a Mosquitto operator

Opened: 2026-09-01. Companion: [`INITIAL_QUESTIONS.md`](INITIAL_QUESTIONS.md) — the decisions this
plan is waiting on, by ID. Where this plan proposes something, the `Qn` reference names the
question that can overturn it.

Nothing below is implemented. The phases are ordered so that each one is releasable on its own and
so that no phase has to be undone by the next.

---

## 0. Scope re-cut (2026-10-05)

**High availability and everything multi-replica is parked** in [`HA_RESEARCH.md`](HA_RESEARCH.md)
section 7: section 3.1 and 3.3 of this plan, phases 4 and 5, and questions Q1, Q2, Q3, Q4, Q15,
Q22 and the PDB half of Q17. `spec.replicas` keeps its `v0.1.0` meaning (independent brokers, no
shared state) until that ticket is reopened; nothing in the phases below may build on `replicas > 1`.

The goal of the next release is narrower and concrete: **an operator that makes it easy to run a
Mosquitto broker from Git through Flux**, driven by one real migration — moving the MQTT broker of
a Home Assistant installation into the cluster. What makes that migration hard today is the
requirement set:

| ID | Requirement | Where the plan answers it |
|---|---|---|
| R1 | Several users, each with its own read / write / readwrite permissions per topic | Phase 2, Q5–Q8, Q11 |
| R2 | Each user's password lives in **its own Secret**, as a plaintext value a client can consume too | Phase 2, Q12 — rules out Q12 option B as the only path |
| R3 | Changes apply themselves: a changed password Secret, an added/removed user, a changed ACL reach the running broker without a manual step | Phase 2, Q13, Q23 |
| R4 | A changed pod label (or annotation) on the CR reaches the pods | Phase 1 |
| R5 | Flux can tell success from failure: every kind reports `observedGeneration` and a `Ready` condition (kstatus), so `dependsOn` and health checks work, and a Secret applied after the CR that needs it converges without a manual reconcile | Phase 1 and 2, cross-cutting |
| R6 | Maximum pod security from the first release: no root, no capabilities, restricted Pod Security Standard for **every** container the operator renders, including any sidecar or initContainer added later | Cross-cutting; constrains Q13 |

R6 is already true for what exists, read out of the tree on 2026-10-05: broker pods set
`runAsNonRoot`, uid/gid/fsGroup `1883`, `RuntimeDefault` seccomp, no ServiceAccount token, and the
container drops `ALL`, forbids privilege escalation and runs a read-only root filesystem
([`internal/builder/statefulset.go`](../../internal/builder/statefulset.go)); the operator pod
carries the same block on both install paths
([`deploy/helm/.../deployment.yaml`](../../deploy/helm/mosquitto-operator/templates/deployment.yaml),
[`config/manager/manager.yaml`](../../config/manager/manager.yaml)). What checks it today is a unit
test that spells the restricted rules out by hand
([`internal/builder/statefulset_test.go`](../../internal/builder/statefulset_test.go)); **no API
server's PodSecurity admission has ever judged a rendered pod.** The requirement therefore adds
one guard: an E2E (or integration, by creating a Pod from the built template directly) run in a
namespace labelled `pod-security.kubernetes.io/enforce=restricted`, observed failing on purpose
per [ADR 0010](../adr/0010-a-check-is-not-a-check-until-it-has-failed-on-purpose.md).

R6 bounds the phase-2 design: every new container runs as uid `1883`, drops `ALL` and adds
nothing. A sidecar that signals the broker (Q13 option C) must manage that **without `CAP_KILL`** —
which Linux allows only when sender and receiver share a uid, so the sidecar runs as `1883` too.
That is the kernel rule as documented, **not measured** in a pod with `shareProcessNamespace`;
measuring it is a phase-2 entry condition.

R5 is derived, not stated: it is what "easy through Flux" means mechanically. `Mosquitto` already
carries `observedGeneration` and a single `Ready` condition
([`api/v1/mosquitto_types.go`](../../api/v1/mosquitto_types.go)); every new kind inherits that.

R3 is the one that collides with an existing decision: [ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md)
says the operator does **not** watch the TLS Secret, so a rotation reaches pods only on restart.
Whether "a Secret changes" includes the TLS Secret is Q23.

---

## 1. Where the project actually is

Shipped in `v0.1.0`. One `Mosquitto` produces four objects — ConfigMap, headless Service, client
Service, StatefulSet — and the generated `mosquitto.conf` carries one listener and
`allow_anonymous true`.

Read out of the tree on 2026-09-01:

| Capability | State |
|---|---|
| Broker pods, config, services, storage, anti-affinity | Implemented |
| TLS on the listener, from a Secret the operator never owns | Implemented ([ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md)) |
| Pod labels / annotations from the CR | **Not implemented** — the operator sets its own labels only |
| Authentication, ACLs, users | **Not implemented** — every broker is anonymous ([ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)) |
| High availability | **Not implemented, and not achievable in the current shape** — `replicas: N` is N independent brokers |
| Metrics exporter | **Decided, nothing built** ([ADR 0002](../adr/0002-the-metrics-exporter-is-written-here.md)) |
| PDB, NetworkPolicy, ServiceMonitor, webhooks | Not in the tree, deliberately |

And the thing that has to be said before any plan: **nothing in this repository has ever been
observed running against a real cluster.** The E2E suite exists and CI runs it; no result of it
has been read by a human as evidence of a working product.

---

## 2. Measured ground

Everything in this section was measured on **2026-09-01** against `eclipse-mosquitto:2.1.2-alpine`
(the pinned image, [ADR 0007](../adr/0007-one-broker-image-pin-and-why-not-the-openssl-tag.md)) on
docker 28.4.0, arm64. It is here because most of section 4 follows from it rather than from
preference, and because several of these are things a reasonable person would assume the other way.

### M1 — What the pinned image contains

```
/usr/lib/mosquitto_acl_file.so
/usr/lib/mosquitto_dynamic_security.so
/usr/lib/mosquitto_password_file.so
/usr/lib/mosquitto_persist_sqlite.so
/usr/lib/mosquitto_sparkplug_aware.so
/usr/bin/mosquitto_ctrl  /usr/bin/mosquitto_passwd
/usr/bin/mosquitto_pub   /usr/bin/mosquitto_sub  /usr/bin/mosquitto_rr
```

All five 2.1 plugins ship in the image. No custom image is needed for any option in this plan.

### M2 — The file plugins reload on SIGHUP. This is the finding the plan turns on.

Broker running with `password-file` and `acl-file` plugins and one user `alice`. A second user
`bob` was appended to both files while the broker ran:

```
--- bob BEFORE sighup ---   rejected
--- bob AFTER  sighup ---   accepted
```

**A user added to the files becomes usable after `kill -HUP`, with no restart and no dropped
connections.** The plugin binaries carry `password_file__reload` and `acl_file__reload` symbols,
so this is the designed behaviour and not an accident of caching.

### M3 — Dynamic security does *not* re-read its file on SIGHUP

Same experiment against `mosquitto_dynamic_security.so`: a client was created over MQTT, the JSON
on disk was then edited to rename that client, and SIGHUP was sent. The broker logged
`Reloading config.` and **kept serving the old in-memory state** — the renamed client was still
accepted under its old name.

Consequence: with dynsec, a file the operator renders is read exactly once, at process start.
Every user change is either a broker restart or a live MQTT command. There is no declarative
middle path.

### M4 — Dynamic security changes over MQTT are immediate

Creating a client, a role and its ACLs through `mosquitto_ctrl ... dynsec ...` against the running
broker made that client usable at once, no restart. **The control-plane path works**; it is the
file path that does not. This is what keeps dynsec a live option rather than a rejected one.

### M5 — `mosquitto_ctrl -f <file>` is a trap

Offline file mode is documented for `dynsec init` and `dynsec setClientPassword` only. Anything
else **exits 0 and silently does nothing**:

```
mosquitto_ctrl -f ds.json dynsec createClient sensor1 -p pw1 ; echo rc=$?
rc=0
grep -o '"username":"[^"]*"' ds.json
"username":"admin"          <- sensor1 was never created
```

An implementation that shells out to `mosquitto_ctrl -f` to render dynsec state would report
success and produce an empty ACL set. If dynsec is ever chosen (Q11), the JSON has to be rendered
in Go from the documented schema, not through the CLI.

### M6 — Kubernetes cannot produce a file Mosquitto will accept in future versions

Same broker, same config, three file states:

| Owner / mode | Broker output |
|---|---|
| `root:mosquitto`, `0644` — what a Secret volume produces | `Warning: File ... has world readable permissions. Future versions will refuse to load this file.` **+** `Warning: File ... owner is not mosquitto.` |
| `root:mosquitto`, `0640` — `defaultMode` plus `fsGroup: 1883` | `Warning: File ... owner is not mosquitto. Future versions will refuse to load this file.` |
| `mosquitto:mosquitto`, `0600` | clean |

Kubernetes writes Secret and ConfigMap volume files owned by **root**; `fsGroup` sets the group and
never the owner. **The clean row is unreachable from a projected volume.** Direct mounting works on
2.1 and is on a stated path to breaking, which makes the copy-into-an-emptyDir step (Q13) a
requirement rather than a nicety.

Related, and the reason the first attempt at M2 failed: the broker drops privileges to user
`mosquitto`, and a root-owned `0640` file produced
`Error loading Dynamic security plugin config: File is not readable - check permissions.`

### M7 — Plugin option names, and the one that works

Brute-forced against the running broker. Only one spelling is accepted:

```
plugin_opt_password_file  =>  Plugin password-file loaded.
plugin_opt_passwordfile   =>  password-file: Error: Unknown option 'passwordfile'.
plugin_opt_pwfile         =>  password-file: Error: Unknown option 'pwfile'.
plugin_opt_filename       =>  password-file: Error: Unknown option 'filename'.
plugin_opt_path           =>  password-file: Error: Unknown option 'path'.
plugin_opt_file_name      =>  password-file: Error: Unknown option 'file_name'.
plugin_opt_pw_file        =>  password-file: Error: Unknown option 'pw_file'.
```

The 2.1 shape, which is also what replaces the deprecated `per_listener_settings`:

```
plugin_load  pwfile /usr/lib/mosquitto_password_file.so
plugin_opt_password_file /mosquitto/auth/passwd
plugin_load  aclfile /usr/lib/mosquitto_acl_file.so
plugin_opt_acl_file /mosquitto/auth/acl

listener 1883
listener_allow_anonymous false
plugin_use pwfile
plugin_use aclfile
```

`plugin_load` declares, `plugin_use` binds to the enclosing listener, `global_plugin` binds to all
of them.

### M8 — `--test-config` is a syntax gate, not a correctness gate

```
mosquitto -c ok.conf  --test-config  ->  Configuration file is OK.        rc=0
mosquitto -c bad.conf --test-config  ->  Error: Unknown configuration variable 'this_is_garbage'.
                                         Error found at /tmp/bad.conf:2.  rc=3
```

Useful: a real exit code and a file:line. **But** it validates directive names only, and nothing a
plugin decides. Two configs that `--test-config` passed with `rc=0` and that then failed at
startup:

```
password-file: Error: Unknown option 'file'.                              # M7, bad plugin option
Error: `persistence true` cannot be used with a persistence plugin.       # persistence + persist-sqlite
```

So it catches typos in directives and not mistakes in plugin wiring. An initContainer running it
(Q14) is worth having and is not a correctness gate.

### M9 — Dynsec bootstraps itself when its file is missing

```
Dynamic security plugin config not found, generating a default config.
  Generated passwords are at /tmp/ds.json.pw
```

A generated admin credential appearing on disk next to the config is a fact worth knowing about
before it appears in a pod.

### M12 — TLS material reloads on SIGHUP; a broken pair breaks the listener, not the process

*Measured 2026-10-05*, `eclipse-mosquitto:2.1.2-alpine`, docker 28.4.0, arm64. Listener 8883 with
`certfile`/`keyfile` on a bind mount; two certificates `CN=broker-a` and `CN=broker-b` from one
test CA. Certificate subject as served to a fresh `openssl s_client`:

```
--- served at start ---                 subject=CN=broker-a
--- files swapped, BEFORE sighup ---    subject=CN=broker-a
--- AFTER sighup ---                    subject=CN=broker-b
```

A subscriber connected over TLS **before** the reload stayed connected and received a message
published after it (`t/x after-reload`). So a renewed certificate reaches new connections on
SIGHUP, with no restart and no dropped clients.

The failure case — certificate `b` with key `a`, then SIGHUP:

```
OpenSSL Error [0]: error:05800074:x509 certificate routines::key values mismatch
Error when reloading certificate '/m/tls/tls.crt' or key '/m/tls/tls.key'.
OpenSSL Error while trying to get the error[0]: error:0A0000C1:SSL routines::no shared cipher
Client ::1 [::1:49176] disconnected: Protocol error.
```

The process keeps running and existing connections survive, but **the broker does not fall back
to the previous certificate: every new TLS handshake fails** until a valid pair arrives and a
second SIGHUP is sent — which recovered it (`subject=CN=broker-b`, the pre-existing subscriber
received `after-recovery`). Consequence: whatever sends the SIGHUP must check that certificate and
key match **before** signalling, or a half-edited Secret takes the listener down.

### M13 — An ACL of `#` grants nothing under `$`

*Measured 2026-10-05*, same rig, `password-file` + `acl-file` plugins, `listener_allow_anonymous
false`. User `wide` holds `topic readwrite #`; user `admin` holds explicit `$` grants.

| Probe | Result |
|---|---|
| `wide` subscribes `$SYS/broker/version` | `Timed out` — nothing delivered |
| `admin` (`topic read $SYS/#`) subscribes the same | `mosquitto version 2.1.2` |
| `wide` publishes `$custom/x`; `admin` (`topic readwrite $custom/#`) listens | not delivered; `admin`'s own publish to the same topic is |
| `wide` subscribes `$custom/#` explicitly; `admin` publishes | nothing delivered |
| `wide` round trip on `home/x` | delivered (control) |

So `#` in an ACL does not reach `$`-prefixed topics, in either direction; only an ACL entry that
itself starts with `$` does. A publish to `$CONTROL/probe` was not delivered even to a user with
an explicit `$CONTROL/#` grant, so that probe says nothing about ACLs and is not counted. Not
measured: shared subscriptions (`$share/<group>/<topic>`) and which topic their ACL check uses.

### M14 — A reload revokes: removed users and changed passwords are disconnected at once

*Measured 2026-10-05*, same rig, file plugins, `listener_allow_anonymous false`. User `victim`
held a subscription and a publishing connection; user `watcher` held a subscription throughout.

| Change, then SIGHUP | `victim`'s existing connections | Reconnect | `watcher` |
|---|---|---|---|
| `victim` removed from `passwd` and `acl` | `Client vsub ... disconnected.` / `Client vpub ... disconnected.` right after `Reloading config.` | `Connection Refused: not authorised` | untouched (0 disconnects), kept receiving |
| `victim`'s password changed | connection made with the old password disconnected | old password refused | untouched |
| `victim`'s ACL narrowed from `home/#` to `home/allowed/#` | **stays connected** | — | untouched |
| … and then a message to `home/forbidden` and one to `home/allowed/x` | only `home/allowed/x` delivered | — | both delivered |

So the broker re-checks every connected client's credentials on reload and drops exactly those
that no longer authenticate; ACLs are evaluated per message, so a narrowed ACL takes effect on a
live connection without dropping it. **Q10's premise — "an existing connection survives a reload"
— was wrong.** Revocation latency is the time until the reload, i.e. the kubelet's Secret
propagation plus the sidecar, not "until the client reconnects".

### M15 — `spec.config` can open an anonymous listener; a global `allow_anonymous` cannot

*Measured 2026-10-05*, same rig. The generated file (listener 1883, `listener_allow_anonymous
false`, both plugins bound) with one appended block, anonymous `mosquitto_pub` against each port:

| Appended (as `spec.config` would) | Port 1883 | Port 1884 |
|---|---|---|
| `allow_anonymous true` | refused | no listener |
| `listener 1884` + `listener_allow_anonymous true` | refused | **ACCEPTED** |
| `listener 1884` alone | refused | refused |

A global `allow_anonymous true` does not override the listener-scoped `false`. A second listener
with `listener_allow_anonymous true` is a full bypass: it has no plugin bound, so its anonymous
clients are subject to no ACL, and messages cross listeners inside one broker — every user's
topics are readable and writable from it. It is not in the Service, but it is on the pod IP, and
nothing ships a NetworkPolicy. Side observation: with files at `mosquitto:root 0600` the broker
warns `group is not mosquitto. Future versions will refuse to load this file.` — the sidecar's
copy must land as `1883:1883`, which `runAsGroup: 1883` gives for free.

### M16 — Anonymous next to users: what `listener_allow_anonymous true` actually grants

*Measured 2026-10-05*, same rig, file plugins bound to the listener, `listener_allow_anonymous
true`, users `alice` (`readwrite home/#`) and a reference user `ref` (`readwrite #`).

| Probe | ACL without a general section | ACL with `topic read pub/#` before the first `user` line |
|---|---|---|
| anonymous client connects | yes (`New client connected ... as auto-...`, no username) | yes |
| anonymous read / write `home/x` | no / no | no / no |
| anonymous read / write `pub/x` | no / no | **yes** / no |
| `alice` read `pub/x` | no | **no** — the general section is not inherited by users |
| `alice` with a wrong password, no password; unknown user `ghost` | `Connection Refused: not authorised` | same |

So: anonymous access is deny-by-default under `acl-file`; the section before the first `user` line
is the anonymous ACL and applies to anonymous clients only; a client that presents a username is
never downgraded to anonymous when its credentials fail.

### M17 — Client-ID takeover crosses identities; `use_username_as_clientid` stops it and excludes anonymous

*Measured 2026-10-05*, same rig. `alice` holds a persistent session (`-c`, QoS 1, client ID
`z2m`), goes offline, `ref` publishes `queued-for-alice` to it.

Without countermeasure, `listener_allow_anonymous true`, anonymous ACL `readwrite home/#`:

| Probe | Result |
|---|---|
| anonymous connects with client ID `z2m`, clean session off | **receives `queued-for-alice`** — the queued message of `alice`'s session |
| `alice` reconnects as `z2m` | nothing — the message is gone |
| `alice` online as `z2m`; anonymous connects as `z2m` | `Client z2m ... disconnected: session taken over.`, then the two kick each other in a loop |
| **authenticated** `ref` connects as `z2m` | `alice` kicked the same way — **independent of anonymous access** |

With `use_username_as_clientid true` on the listener:

| Probe | Result |
|---|---|
| anonymous connects (any client ID) | `Connection Refused: not authorised` — **anonymous is refused outright although `listener_allow_anonymous true`** |
| `ref` connects with client ID `alice` | connected as `ref`; `alice`'s session untouched |
| `alice` reconnects with client ID `whatever` | receives `queued-for-alice` |
| `alice` holds two connections at once | they take each other over in a loop — one connection per username |

Consequences: any client that may connect can take over another client's session by guessing its
client ID — disconnecting it and receiving its queued messages, subject to its own ACL (anonymous
had `home/#` here; whether the ACL gates the stolen delivery otherwise is not measured).
`use_username_as_clientid` closes that across users, costs "one connection per username", and
cannot coexist with anonymous access on the same listener.

### M18 — The pinned image hashes `$7$` with 1000 iterations; argon2id does not work

*Measured 2026-10-05*, `eclipse-mosquitto:2.1.2-alpine`. `mosquitto_passwd`'s help says
`Defaults to argon2id, which is recommended.` What it does:

```
mosquitto_passwd -c -b p a pw                      -> a:$7$1000$<88 b64 salt>$<88 b64 hash>
mosquitto_passwd -H sha512-pbkdf2 -b p b pw        -> b:$7$1000$...
mosquitto_passwd -H argon2id -c -b p a pw          -> Error: Unable to hash password.  rc=10
```

The default silently falls back to PBKDF2-SHA512 (`$7$`) at **1000 iterations**. `libargon2.so.1`
is present in `/usr/lib`, but neither `mosquitto_passwd` nor `mosquitto_password_file.so` links it,
and the plugin's unresolved symbols include `PKCS5_PBKDF2_HMAC` and nothing from argon2. So the
broker can verify `$7$`; that it **cannot** verify `$argon2id$` is inferred from the symbols, not
login-tested. The catalogue's earlier "2.1 also understands `$argon2id$`" does not hold for this
build. Not measured: whether the plugin honours an iteration count other than 1000 written into
the `$7$<iterations>$` field.

### Not measured

- Anything on a real cluster. All of the above is single-container docker.
- Bridge behaviour, loop handling, retained-message propagation across a bridge.
- `persist-sqlite` under load or crash.
- Whether SIGHUP reload is atomic with respect to a half-written file — **this matters** and is
  listed as a phase-2 verification item, not an assumption.
- Reload behaviour of the file plugins on the `arm64`/`amd64` split; measured on arm64 only.

---

## 3. The three problems, honestly stated

### 3.1 High availability

*Parked 2026-10-05 in [`HA_RESEARCH.md`](HA_RESEARCH.md) section 7 — see section 0. Kept here
unchanged as the record of why.*

**Open-source Mosquitto has no clustering.** No shared sessions, no shared retained messages, no
replication, no failover. This is not a gap in our implementation; it is the upstream product.
The commercial edition sells clustering precisely because the open one does not have it.

So `replicas: 3` today is three brokers that do not know about each other:

- a retained message published through pod 0 does not exist on pod 1;
- a persistent session established on pod 1 is stranded there — the client reconnects, the Service
  sends it to pod 2, and its subscriptions and queued messages are gone;
- a subscriber on pod 0 never receives what a publisher sent to pod 2.

The Service in front of them makes this worse, not better, by hiding which broker a client got.

What is actually reachable is **one active broker with a bounded, predictable failover**, and the
size of that bound is a product decision (Q3), not an engineering one. Everything else — bridge
meshes, elected primaries — buys a specific property at a specific cost and should be built only
after somebody has said which property they need.

The plan therefore treats HA as **a promise to be narrowed and then held**, and the first
deliverable is the narrowing (Q1, Q2) rather than a mechanism.

*Added 2026-09-01:* [`HA_RESEARCH.md`](HA_RESEARCH.md) surveys the field (broker clustering,
log-backed brokers, standby flips, bridges, LB tricks) and adds measurements M10/M11: a bridged
pair replicates retained state and survives the primary's death, and sessions provably do not
cross. It sharpens Q1's option D into the "flip pair" and adds Q22 (fencing).

### 3.2 Users and ACLs at runtime

The requirement — many clients, differing permissions, arriving while the broker runs — rules out
anything that needs a restart per change. M2 says the file plugins deliver that; M3 says a
file-rendered dynsec does not.

`valkey-operator` offers no pattern to copy here: its `AuthSpec` is a Secret name and a key,
because Valkey in that deployment has one password and no per-client authorisation. This is the
one area where the mosquitto operator has to design rather than adapt, and the reason to prefer
separate objects is not symmetry with anything — it is that a `MosquittoUser` and a `Mosquitto`
have different lifecycles, different writers, and must have different RBAC (Q5).

The RBAC point deserves its own sentence, because it is the strongest argument and the easiest to
miss: **edit on a `Mosquitto` is total control of the broker today**, since `spec.config` is
appended verbatim. If users are a field on that object, delegating "may add a client" delegates
"may rewrite the broker configuration". A separate kind is the only way to split those.

### 3.3 Version updates

*Parked 2026-10-05 with HA — see section 0.*

**"Without interrupting operation" is not achievable and the goal has to be reworded.** A
StatefulSet has no surge, so a version change stops the process and starts another; there is no
window where both serve. What is achievable, and worth committing to as the actual goal:

> **No message loss, and a bounded, measured reconnect gap.**

That holds when the session and its queued messages survive on a PVC and the client publishes at
QoS 1 or 2. It does not hold for QoS 0, for clean sessions, or for clients that do not reconnect —
and no operator can fix those from this side. Saying so in the README is part of the deliverable
(Q15).

---

## 4. Target shape

Proposed, gated on section B of the questions.

### 4.1 Kinds

| Kind | Scope | Holds |
|---|---|---|
| `Mosquitto` | Namespaced | The broker: topology, storage, TLS, listener, and the policy for who may attach to it |
| `MosquittoRole` | Namespaced | *Deferred 2026-10-05 (Q6) — not in the first release.* A named set of ACLs. Referenced by many users. Mirrors the dynsec object model so a later re-decision maps across |
| `MosquittoUser` | Namespaced | One principal: username, a reference to its credential, role references and/or an inline ACL list, and a `brokerRef` |

Reference direction is **user → broker** (Q7), with the broker holding an acceptance policy rather
than a list, so onboarding a client never needs write access to the broker object.

### 4.2 The reconcile, end to end

*Re-cut 2026-10-05 after Q5, Q11, Q12 and Q13: no roles yet, the operator reads the users'
password Secrets, and exactly one Secret per broker carries the rendered state.*

```
Secret (n)  username + plaintext password, one per user, written by the user (R2, Q24)
   │  get;list;watch  (scope: open question)
   v
MosquittoUser (n) ─┐
Mosquitto      (1) ┴─> render, sorted and deterministic (Q9),
                       existing salt+hash kept while the plaintext still verifies
                             │
                             v
                Secret <name>-auth   (ONE per broker, operator-owned, ensureOwned)
                  passwd : $7$ hashes only
                  acl    : rendered ACL file
                             │  volume mount, NOT a subPath, NOT in the pod-template hash
                             v
          ┌─────────────────────────────────────────────────────┐
          │ broker pod  (restricted PSS, every container 1883)  │
          │                                                     │
          │  [auth-init]     on EVERY start: copy -> emptyDir   │
          │                  as 1883/0600 (M6)                  │
          │  [auth-sidecar]  kubelet swaps ..data -> compare    │
          │                  bytes -> write temp + rename ->    │
          │                  SIGHUP the broker (M2)             │
          │                          v                          │
          │  /mosquitto/auth/{passwd,acl}            (emptyDir) │
          │                          ^                          │
          │  [mosquitto]  plugin_opt_password_file              │
          │               plugin_opt_acl_file             (M7)  │
          └─────────────────────────────────────────────────────┘
```

**A new user, a removed user, a changed ACL or a changed password does not restart the pod.** The
operator rewrites `<name>-auth`; the kubelet refreshes the mounted files (delay bounded by the
kubelet sync period, per Kubernetes documentation, **not measured** here); the sidecar copies and
signals; the broker reloads. Other clients' connections are not dropped; a client whose
credential was removed or whose password changed **is** disconnected on that reload, and a
narrowed ACL applies to live connections at once (M14). The pod
rolls only for what already rolls it today: `mosquitto.conf` (config hash), the pod template
(pod-spec hash), and from phase 1 the pod labels and annotations.

Properties this shape has, and why it was chosen:

1. **One writer, one path.** The operator renders; nothing writes back; the broker has exactly one
   way to learn its users, at start and at runtime alike. Reconciliation is render-and-compare.
2. **The broker starts without the operator.** `<name>-auth` holds the full state, so a pod that
   restarts while the operator is down comes up with every user — no window of rejected clients.
3. **No new exposure from the aggregate.** `<name>-auth` sits in the same namespace as the
   plaintext Secrets it is rendered from (Q8: same namespace only), so whoever can read it could
   already read the plaintext. The hashes reveal nothing that was not already reachable.
4. **The file the broker reads is owned correctly** (M6), which a direct mount can never be.

Its costs, stated:

- **The operator gains `secrets` authority** it does not hold today: `get;list;watch` on the
  password Secrets and `create;update` for `<name>-auth`. A compromised operator can read and
  overwrite every Secret the grant covers. The scope is an install-time setting, `all` or a
  namespace list (Q12); the default is `all`.
- `shareProcessNamespace: true` and a second container in every broker pod; the sidecar signals as
  uid `1883` without `CAP_KILL` (R6) — kernel rule, **not measured** in a pod.
- Revocation is not instant: it waits for the kubelet to propagate `<name>-auth` and for the
  sidecar's reload; from there it is immediate (M14). The propagation delay is not measured.

The sidecar is the same problem valkey's `internal/tlsmaterial` reloader (in the `valkey-operator`
repository) solves one layer up — including the detail that kubelet updates a mounted Secret by
swapping the `..data` symlink, so change detection compares bytes and not mtimes.

### 4.3 What the generated config gains

```
plugin_load pwfile  /usr/lib/mosquitto_password_file.so
plugin_opt_password_file /mosquitto/auth/passwd
plugin_load aclfile /usr/lib/mosquitto_acl_file.so
plugin_opt_acl_file /mosquitto/auth/acl

listener 8883
certfile /mosquitto/tls/tls.crt
keyfile  /mosquitto/tls/tls.key
listener_allow_anonymous false      # replaces allow_anonymous true; never true (Q26)
use_username_as_clientid true       # no cross-user session takeover (Q27, M17)
plugin_use pwfile
plugin_use aclfile
```

`listener_allow_anonymous` rather than `allow_anonymous`, because the global option is tied to the
deprecated `per_listener_settings` model and the listener-scoped one is what 2.1 steers toward
(Q16 — generate only what survives 3.0).

### 4.4 Built so that a dynsec mode can be added later

*Decided 2026-10-05 (Q11): file plugins now; dynamic security later as an opt-in broker mode,
`spec.auth.mode: files | dynsec`, default `files`. The field is **not** added now — adding it later
is additive. What the implementation must keep true now so that "later" stays cheap:*

| Constraint | Why it matters for dynsec |
|---|---|
| The `MosquittoUser` API says nothing file-specific: a `credentialsSecret` (name, `usernameKey`, `passwordKey`; Q24), ACL entries as topic + `read`/`write`/`readwrite` (+ pattern) | The same objects must render to the dynsec JSON without a user-visible change. A field shaped after `acl_file` syntax would have to be migrated. |
| Rendering is one function from desired state to a mode-specific payload; delivery is a separate step | dynsec swaps the payload (`dynamic-security.json` instead of `passwd`/`acl`) and adds a second delivery path (MQTT commands for live changes); the render-and-compare core stays. |
| `<name>-auth` stays the single per-broker Secret, keys named per payload | dynsec adds its JSON and the operator's admin credential as further keys — still one Secret, still the start-without-operator property. |
| Salt + hash preservation lives in the renderer, not in the file format | dynsec stores salted hashes too; the same keep-while-it-verifies rule prevents churn there. |
| A reserved username prefix is enforced from the first release (Q19) | dynsec needs an operator-owned admin principal; the name must be impossible to claim through a `MosquittoUser` before that principal exists. |
| The auth-init container copies on every start, unconditionally | In dynsec mode the broker writes its own file; copying on every start keeps the Secret authoritative over a stale PVC copy. |

What a dynsec mode adds, recorded so it is not rediscovered: an admin credential with network
reach to every broker pod, a TLS requirement on that path (a plain listener would carry it in
cleartext), drift detection against live state, and three measurements that gate it — the 2.1
dynsec JSON schema including hash fields (render it in Go, never through `mosquitto_ctrl -f`, M5),
whether `kickClient` ends a live session, and whether `setClientPassword` drops existing
connections.

---

## 5. Phases

Each phase is releasable, has its own ADR, and states what proves it works. No phase depends on a
later one.

### Phase 0 — Decide (now)

*Re-cut 2026-10-05 (section 0): the HA ADR left this phase with the HA questions.*

*Status 2026-10-05: every question in scope is answered.* What remains of this phase is writing
the two new ADRs and running the entry measurements of phase 2. Nothing is built. Output:

| ADR | Subject | Rests on (all answered) |
|---|---|---|
| 0011 | `MosquittoUser`: separate kind, same namespace, credentials from the user's Secret (configurable keys), ACL shape, username rules and collisions, `$` refusal, reserved prefix | Q5, Q6, Q7, Q8, Q9, Q19, Q24, Q25 |
| 0012 | File plugins now, dynsec as a later opt-in mode (section 4.4); one aggregate Secret per broker; `$7$`/1000; reload sidecar from the operator image; TLS reload; `secrets` grant modes and default; never anonymous; `use_username_as_clientid`; `spec.config` allowlist | Q10, Q11, Q12, Q13, Q14, Q21, Q23, Q26, Q27, Q28 |

Existing ADRs amended **in place** in the same change as the code, never by a new record:
[0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md) (Q23, in-pod TLS
reload), [0006](../adr/0006-both-install-paths-grant-the-same-authority.md) (the grant table gains
the `secrets` rule in both modes), [0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
(Q26, Q14 — never anonymous, allowlist), and [0002](../adr/0002-the-metrics-exporter-is-written-here.md)
(Q18, when phase 6 is built). Q16, Q17 and Q20 need no ADR of their own; they go into README and
security documentation.

~~0011 — What HA means here, and what `replicas` promises (Q1, Q2, Q3)~~ — parked with
[`HA_RESEARCH.md`](HA_RESEARCH.md); it gets the next free number when that ticket is reopened.
Q23 may amend [ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md)
in place rather than land in 0012.

### Phase 1 — The small gaps (independent of every decision above)

The one field the request names that has no decision attached: **pod labels** (R4). Plus its
obvious sibling, and the restricted-admission guard R6 asks for.

- `spec.podLabels` and `spec.podAnnotations`, merged **under** the operator's own labels so a user
  cannot overwrite a selector label. That merge order is the whole design; it needs a test that
  tries to overwrite `app.kubernetes.io/instance` and asserts the operator's value wins.
- The pod-spec hash already covers the pod template, so a label change rolls the pods
  automatically. Verify that rather than assume it.
- `mosquitto --test-config` in an initContainer (Q14) — cheap, uses a binary already in the image
  (M8), and turns a class of `spec.config` typo from a crash loop into a clear message.

- The initContainer carries the same security context as the broker container (R6).
- The R6 guard: a rendered pod admitted by PodSecurity `enforce=restricted`, and observed rejected
  once with a deliberately weakened security context.

**Proves it works:** unit tests on the merge order; an integration test that the API server accepts
the built pod; an E2E that a label change rolls the StatefulSet; the R6 guard above.

### Phase 2 — Users and ACLs

The core of the request. Gated on phase 0.

1. The `MosquittoUser` CRD (Q5 answered 2026-10-05; `MosquittoRole` deferred, Q6), RBAC on both
   install paths, chart templates.
2. A deterministic renderer (Q9): users (and roles, once Q6 adds them) to a `passwd` and an `acl` file, sorted by
   `(namespace, name)`, with a test that renders one input a hundred times and asserts a single
   distinct output.
3. The reserved-prefix rule (Q19) and the username character set (Q24), enforced at render time —
   the username lives in a Secret, so validation cannot see it.
4. Every user ACL entry starting with `$` is refused, in CEL validation and again at render time
   (Q8, answered 2026-10-05; M13 shows `#` does not reach `$` topics, so no deny lines are needed).
5. The auth init container and sidecar (Q13 answered): copy as 1883/0600 on every start, then
   watch the `<name>-auth` mount, write temp-then-rename, SIGHUP. The same sidecar watches the TLS
   mount (Q23) and signals only after a cert/key match check (M12); it is present whenever
   `spec.tls` or users are configured.
6. The generated config gains the plugin block and `listener_allow_anonymous false`.
7. Same-namespace binding only (Q8, first half answered 2026-10-05); a cross-namespace acceptance
   policy on the broker is a later, additive opt-in.
8. *Added 2026-10-05 (Q14):* the `spec.config` directive allowlist, enforced at render time — the
   release that adds authentication must not ship with the M15 bypass open. ADR 0008 amended in
   place in the same change.
9. *Added 2026-10-05 (R2, R3, R5):* a password Secret that is created after the user that names it,
   or changed later, reaches the broker without a manual step; the user reports a `Ready=False`
   condition with a readable reason while its Secret is missing. Which component watches the
   Secret — operator or in-pod helper — is Q12/Q13.


**Proves it works — and one of these is not optional:**

- Unit: render determinism, merge order, reserved prefix rejection, a `$`-prefixed ACL entry refused
  at render time even when validation was bypassed.
- Integration: the CRDs are accepted; owner references are accepted as written.
- **E2E: a `MosquittoUser` created against a running broker becomes able to publish without the
  broker pod restarting.** That single test is the whole phase. If it does not pass, the design is
  wrong and no amount of unit coverage says otherwise.
- **E2E: a client is rejected on a topic outside its ACL.** A permission system with no negative
  test is a permission system nobody has checked.
- **E2E: changing the value in a user's password Secret makes the old password fail and the new
  one work, with no edit to any CR.** That is R3 as a test; without it "applies itself" is a claim.
- **The half-written-file question** from section 2: write a large `passwd` and SIGHUP mid-write,
  and find out whether the plugin can observe a partial file. If it can, the copy has to be
  write-to-temp-then-rename, and that has to be known before this ships rather than after.

Per [ADR 0010](../adr/0010-a-check-is-not-a-check-until-it-has-failed-on-purpose.md), each new
guard is trusted only after being observed failing against a deliberately broken tree, with the
exact message recorded.

**Entry measurements** — collected 2026-10-05 from the answers; each is run against the pinned
image (or a Kind cluster where noted) before the code that depends on it is written:

| # | Measurement | Gates | Source |
|---|---|---|---|
| E1 | A `$7$1000$…` line rendered in Go is accepted by the broker; byte format identical to `mosquitto_passwd` | renderer | Q28, M18 |
| E2 | `passwd` and `acl` parse usernames containing `@` and `.` | username allowlist | Q24 |
| E3 | A sidecar running as uid `1883` with all capabilities dropped can SIGHUP the broker across `shareProcessNamespace`, under PodSecurity `restricted` (Kind) | sidecar | R6, Q13 |
| E4 | Kubelet propagation delay of a changed Secret volume, and that `tls.crt`/`tls.key` swap together via `..data` (Kind) | revocation latency in docs, Q23 | M14, Q23 |
| E5 | Broker behaviour with a mismatched TLS pair at startup | sidecar init path | M12 |
| E6 | A 2.0 image fails `--test-config` on the generated file, with file and line | Q16 documentation | Q16 |
| E7 | The initial `spec.config` allowlist, taken from `mosquitto.conf(5)` of 2.1.2 | allowlist | Q14 |

Measured on 2026-10-05 and no longer an entry condition: password change of an existing user on
SIGHUP (M14), ACL change on a live connection (M14), TLS reload (M12), `$` topics under `#` (M13),
anonymous and client-ID takeover behaviour (M16, M17).

### Phase 3 — Bound the exposure

*Dropped 2026-10-05:* the operator ships no NetworkPolicy (Q17), and the `spec.config` allowlist
moved into phase 2 (Q14). What remains of this phase is documentation — a user-written
NetworkPolicy example in the README and the exposure in the security documentation — and it ships
with phase 2.


Authentication without reachability control leaves half the story. NetworkPolicy (Q17), opt-in,
with the same shape valkey uses. ~~Plus the `spec.config` deny-list for directives that can undo the
auth phase 2 just added.~~ *Moved 2026-10-05 (Q14): an allowlist, and it ships in phase 2.*

### Phase 4 — The upgrade promise

*Parked 2026-10-05 in [`HA_RESEARCH.md`](HA_RESEARCH.md) section 7.*

Make the reworded goal from 3.3 true and measured.

- Persistent-session survival across a broker restart, on a PVC.
- Tuned termination and readiness so the gap is the broker start and nothing else.
- **E2E: publish at QoS 1 across an image change and assert zero message loss**, and record the
  reconnect gap as a number that goes in the README.
- PDB (Q17), opt-in, designed against Q3's number.

### Phase 5 — HA, if Q3 says the bound is not good enough

*Parked 2026-10-05 in [`HA_RESEARCH.md`](HA_RESEARCH.md) section 7.*

Only reached if the measurement from phase 4 fails the requirement from Q3. Option D (elected
primary) or option C (bridge mesh) from Q1, each with its own ADR. The concrete candidate for D
is the flip pair of [`HA_RESEARCH.md`](HA_RESEARCH.md) section 4 — measured for retained-state
survival (M10) and session loss (M11), gated on the fencing decision (Q22). This phase is deliberately last
and deliberately conditional: it is the most expensive thing in the plan and the one most likely to
turn out unnecessary.

### Phase 6 — The exporter

[ADR 0002](../adr/0002-the-metrics-exporter-is-written-here.md) as written, plus the reserved
`$SYS` principal from Q18. It sits last because it is the only item with a complete design and no
open question — which makes it the safest thing to defer.

---

## 6. What this plan is betting on, and how it breaks

| Bet | If it is wrong |
|---|---|
| SIGHUP reload of the file plugins is atomic enough for a copied file (M2 measured; atomicity **not** measured) | The copy becomes write-temp-then-rename. Cheap, but it must be found in phase 2, not in production. *2026-10-05: temp-then-rename is now the design (Q13), so this bet is no longer taken.* |
| ~~Pre-hashed passwords (Q12 option B) are acceptable onboarding friction~~ | *Lost 2026-10-05 (R2, Q12):* the operator reads the password Secrets; grant scope configurable, default `all`. |
| `acl_file` expressiveness is enough — no ACL priorities | Roles cannot express what someone needs, and Q11 gets re-decided toward dynsec. The role-shaped API is what makes that survivable. |
| ~~Revocation latency (a deleted user keeps its connection until it disconnects) is acceptable~~ | *Settled 2026-10-05 by M14:* a reload disconnects removed users and changed passwords. What remains is the unmeasured kubelet propagation delay. |
| Single-active with a bounded gap satisfies the requirement *(parked with HA, 2026-10-05)* | Phase 5, which is the expensive one. |
| The file plugins are not removed in 3.0 | They are the documented replacement for the deprecated options, so this is a low-probability bet — but it is a bet, and it is on somebody else's roadmap. |

*Re-read 2026-10-05:* revocation turned out to be present (M14) and the atomicity bet is no
longer taken. Worth actively watching now: **the `secrets` grant**, because its default is
cluster-wide read and write; **`spec.config`**, because M15 shows one appended listener undoes all
authentication; and **the kubelet propagation delay**, because it is the revocation latency and
nobody has measured it.
