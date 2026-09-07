# Initial plan: from a broker launcher to a Mosquitto operator

Opened: 2026-09-01. Companion: [`INITIAL_QUESTIONS.md`](INITIAL_QUESTIONS.md) — the decisions this
plan is waiting on, by ID. Where this plan proposes something, the `Qn` reference names the
question that can overturn it.

Nothing below is implemented. The phases are ordered so that each one is releasable on its own and
so that no phase has to be undone by the next.

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
| `MosquittoRole` | Namespaced | A named set of ACLs. Referenced by many users. Mirrors the dynsec object model so a later re-decision maps across |
| `MosquittoUser` | Namespaced | One principal: username, a reference to its credential, role references and/or an inline ACL list, and a `brokerRef` |

Reference direction is **user → broker** (Q7), with the broker holding an acceptance policy rather
than a list, so onboarding a client never needs write access to the broker object.

### 4.2 The reconcile, end to end

```
MosquittoUser (n)  ─┐
MosquittoRole (n)  ─┼─> render, sorted and deterministic (Q9)
Mosquitto      (1)  ┘        │
                             ├─> Secret <name>-auth   : passwd  (hashes only)
                             └─> Secret <name>-auth   : acl
                                        │
                                        │  projected, read-only, root-owned
                                        v
                     ┌──────────────────────────────────────────┐
                     │ broker pod                               │
                     │                                          │
                     │  [auth-sidecar]  watches the projection  │
                     │        │  copies to emptyDir as 1883/0600 (M6)
                     │        │  sends SIGHUP on change          (M2)
                     │        v                                  │
                     │  /mosquitto/auth/{passwd,acl}  (emptyDir) │
                     │        ^                                  │
                     │  [mosquitto]  plugin_opt_password_file    │
                     │               plugin_opt_acl_file    (M7) │
                     └──────────────────────────────────────────┘
```

Three properties this shape has, and they are the reasons to prefer it:

1. **One writer.** The operator renders; nothing writes back. Reconciliation is render-and-compare,
   which is what the rest of this operator already is. Dynsec would make the broker a second writer
   of the same state (M3) and turn this into a bidirectional sync against a live MQTT connection.
2. **No new cluster-wide privilege.** No `pods/exec`, no `secrets` read if passwords arrive
   pre-hashed (Q12 option B). The ClusterRole's most valuable current property — no rule on
   `secrets` at all — survives phase 2.
3. **The file the broker reads is owned correctly** (M6), which a direct mount can never be.

Its cost, stated: `shareProcessNamespace: true` so the sidecar can signal the broker, and a second
container in every broker pod. The sidecar is the same problem valkey's
`internal/tlsmaterial` reloader (in the `valkey-operator` repository)
solves one layer up — including the detail that kubelet updates a projected volume by swapping the
`..data` symlink, so change detection compares bytes and not mtimes.

### 4.3 What the generated config gains

```
plugin_load pwfile  /usr/lib/mosquitto_password_file.so
plugin_opt_password_file /mosquitto/auth/passwd
plugin_load aclfile /usr/lib/mosquitto_acl_file.so
plugin_opt_acl_file /mosquitto/auth/acl

listener 8883
certfile /mosquitto/tls/tls.crt
keyfile  /mosquitto/tls/tls.key
listener_allow_anonymous false      # replaces allow_anonymous true
plugin_use pwfile
plugin_use aclfile
```

`listener_allow_anonymous` rather than `allow_anonymous`, because the global option is tied to the
deprecated `per_listener_settings` model and the listener-scoped one is what 2.1 steers toward
(Q16 — generate only what survives 3.0).

---

## 5. Phases

Each phase is releasable, has its own ADR, and states what proves it works. No phase depends on a
later one.

### Phase 0 — Decide (now)

Answer Q1, Q2, Q5, Q11, Q12 at minimum. Nothing is built. Output is answers in
[`INITIAL_QUESTIONS.md`](INITIAL_QUESTIONS.md) and three ADRs:

| ADR | Subject | Gated on |
|---|---|---|
| 0011 | What HA means here, and what `replicas` promises | Q1, Q2, Q3 |
| 0012 | Users and ACLs are separate objects, and why not inline | Q5, Q6, Q7, Q8 |
| 0013 | The file plugins, not dynamic security — with the named trigger to re-decide | Q10, Q11, Q12, Q13 |

ADR 0011 is the one that changes the README's first paragraph, so it goes first.

### Phase 1 — The small gaps (independent of every decision above)

The one field the request names that has no decision attached: **pod labels**. Plus its obvious
sibling.

- `spec.podLabels` and `spec.podAnnotations`, merged **under** the operator's own labels so a user
  cannot overwrite a selector label. That merge order is the whole design; it needs a test that
  tries to overwrite `app.kubernetes.io/instance` and asserts the operator's value wins.
- The pod-spec hash already covers the pod template, so a label change rolls the pods
  automatically. Verify that rather than assume it.
- `mosquitto --test-config` in an initContainer (Q14) — cheap, uses a binary already in the image
  (M8), and turns a class of `spec.config` typo from a crash loop into a clear message.

**Proves it works:** unit tests on the merge order; an integration test that the API server accepts
the built pod; an E2E that a label change rolls the StatefulSet.

### Phase 2 — Users and ACLs

The core of the request. Gated on phase 0.

1. `MosquittoUser` and `MosquittoRole` CRDs, RBAC on both install paths, chart templates.
2. A deterministic renderer (Q9): users and roles to a `passwd` and an `acl` file, sorted by
   `(namespace, name)`, with a test that renders one input a hundred times and asserts a single
   distinct output.
3. The reserved-prefix rule (Q19), enforced at render time and not only in validation.
4. The mandatory deny on `$CONTROL/#` and `$SYS/#` (Q8), applied to every rendered user regardless
   of what its CR asks for.
5. The auth sidecar (Q13 option C): watch, copy as 1883/0600, SIGHUP.
6. The generated config gains the plugin block and `listener_allow_anonymous false`.
7. Broker acceptance policy for cross-namespace users (Q8), defaulting to same-namespace.

**Proves it works — and one of these is not optional:**

- Unit: render determinism, merge order, reserved prefix rejection, the `$CONTROL` deny surviving a
  CR that explicitly asks for it.
- Integration: the CRDs are accepted; owner references are accepted as written.
- **E2E: a `MosquittoUser` created against a running broker becomes able to publish without the
  broker pod restarting.** That single test is the whole phase. If it does not pass, the design is
  wrong and no amount of unit coverage says otherwise.
- **E2E: a client is rejected on a topic outside its ACL.** A permission system with no negative
  test is a permission system nobody has checked.
- **The half-written-file question** from section 2: write a large `passwd` and SIGHUP mid-write,
  and find out whether the plugin can observe a partial file. If it can, the copy has to be
  write-to-temp-then-rename, and that has to be known before this ships rather than after.

Per [ADR 0010](../adr/0010-a-check-is-not-a-check-until-it-has-failed-on-purpose.md), each new
guard is trusted only after being observed failing against a deliberately broken tree, with the
exact message recorded.

### Phase 3 — Bound the exposure

Authentication without reachability control leaves half the story. NetworkPolicy (Q17), opt-in,
with the same shape valkey uses. Plus the `spec.config` deny-list for directives that can undo the
auth phase 2 just added.

### Phase 4 — The upgrade promise

Make the reworded goal from 3.3 true and measured.

- Persistent-session survival across a broker restart, on a PVC.
- Tuned termination and readiness so the gap is the broker start and nothing else.
- **E2E: publish at QoS 1 across an image change and assert zero message loss**, and record the
  reconnect gap as a number that goes in the README.
- PDB (Q17), opt-in, designed against Q3's number.

### Phase 5 — HA, if Q3 says the bound is not good enough

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
| SIGHUP reload of the file plugins is atomic enough for a copied file (M2 measured; atomicity **not** measured) | The copy becomes write-temp-then-rename. Cheap, but it must be found in phase 2, not in production. |
| Pre-hashed passwords (Q12 option B) are acceptable onboarding friction | The operator needs `secrets` access, which is a real privilege increase and needs its own ADR, ideally namespace-scoped rather than cluster-wide. |
| `acl_file` expressiveness is enough — no ACL priorities | Roles cannot express what someone needs, and Q11 gets re-decided toward dynsec. The role-shaped API is what makes that survivable. |
| Revocation latency (a deleted user keeps its connection until it disconnects) is acceptable | Q11 flips to dynsec on its own, for the `kick` command. This is the single most likely reason to re-decide. |
| Single-active with a bounded gap satisfies the requirement | Phase 5, which is the expensive one. |
| The file plugins are not removed in 3.0 | They are the documented replacement for the deprecated options, so this is a low-probability bet — but it is a bet, and it is on somebody else's roadmap. |

The three that are worth actively watching: **revocation latency**, because it is a security
property and it is absent by construction; **the atomicity question**, because it is unmeasured and
cheap to measure; and **the HA bound**, because it is the only one whose failure costs a phase
rather than a patch.
