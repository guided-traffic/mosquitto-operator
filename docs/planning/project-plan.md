# Project plan

How `v0.1.x` becomes the operator the Home Assistant migration needs: one broker per `Mosquitto`,
run from Git through Flux, with users, permissions and credentials that follow every change on
their own ([ADR 0012](../adr/0012-the-first-release-is-one-broker-run-from-git-and-high-availability-is-parked.md)).

**This file is the work list.** Each phase is worked out to the step: what it builds, which files
and functions it touches, which tests in which tier prove it, which documentation and records it
updates, and when it is done. Phases are built from here directly, not converted into tickets; a
finding goes into the phase that does the work; an open decision goes into the step that needs it
and becomes a record when it is answered. A phase that is done is deleted from this file
([ADR 0011](../adr/0011-documentation-has-five-homes-and-tickets-are-work-lists-that-get-archived.md)
D10, D12). The only tickets are embargoed security findings, which a tracked file cannot carry.

Written 2026-10-05. Phases, not dates; the order is the order of the work, and each phase is
releasable on its own.

## Working agreements

- **Decide before building.** Every decision a step needs is a record already. A question that
  comes up while building is written into the step with its options and put to the owner one at a
  time; the answer becomes a record or an amendment in the same session. Code written on an
  undecided question is speculation.
- **Measure before relying.** A step whose design rests on broker or kubelet behaviour starts with
  the measurement it names; the result goes into
  [docs/developer/broker-behaviour.md](../developer/broker-behaviour.md) with command and output,
  and a result that contradicts a record stops the step until the record is amended.
- **Tests in the cheapest tier that can answer the question**, never in one that cannot — envtest
  starts no pod and collects no garbage. Every new guard is observed failing on purpose first,
  with the message recorded ([ADR 0010](../adr/0010-a-check-is-not-a-check-until-it-has-failed-on-purpose.md)).
- **A step is done with its documentation**: the README reference for every field, value and flag
  it adds, the page under `docs/operations/`, `docs/security/` or `docs/developer/` that describes
  what it built, and the `Status` and index row of every record it builds.
- **The Makefile is the entry point.** `make lint`, `make cyclo` (threshold 15), the test target of
  the tier, and `make generate-all` after any change under `api/v1/` or to an RBAC marker — with
  the generated files committed.
- **The project stays on 0.x.** No commit carries `BREAKING CHANGE` or `!`, and no commit message
  carries an apostrophe.
- **Verification is named.** "Done" means what was run, against what, with what result. Nothing in
  this repository has yet been observed running against a real cluster; phase 6 changes that.

Proposed names below — chart values, flags, labels, reason strings, new packages — are proposals;
a name that becomes API surface is fixed in the step that builds it and written into the README
reference.

## Done

`v0.1.0` to `v0.1.8`: one `Mosquitto` renders a ConfigMap, a headless and a client Service and a
StatefulSet; TLS from an existing Secret; anonymous brokers; two install paths with equal
authority; five test tiers. What it built is in the records 0001 to 0010 and in
[docs/developer/](../developer/README.md).

## Phase 1 — The tree tells the truth, and the release gate is back

**Built 2026-10-05.** The E2E tier gates releases again (first CI run of both legs: `Test and
Release` run `37367556237`), the statements the code contradicted are corrected, the pipeline holds
only the authority it uses, and the ownership refusal message is pinned. What it built is recorded
in ADR 0001, 0003, 0004, 0005, 0007, 0009 and 0010 and in
[ci-and-release.md](../developer/ci-and-release.md). One check is left, because only a merge to
`main` can run it:

- **A release run on `main` with the reduced permissions.** `semantic-release` now holds
  `contents: read` only and writes through the app token; `build` logs out of Docker Hub before the
  pinned SBOM and release actions. **Done when** the first `Test and Release` run on `main` after
  the merge succeeds through `semantic-release`, and the `Release Docker & Helm` run it triggers
  succeeds through `release-helm-gh`. A step that fails for a permission gets the permission back,
  with a comment naming the call that needed it, and ADR 0005 D6 says so.

## Phase 2 — Pod labels, the configuration gate, the admission guard, and the first Secret switch

**Built 2026-10-05**: `spec.podLabels` and `spec.podAnnotations` (ADR 0012 D5), updates that merge
(ADR 0009 D9), the `config-check` init container (ADR 0007 D10, with the measurement M19 it
needed), the PodSecurity admission guard (ADR 0012 D4) and `secretSecurity` for the TLS Secret
(ADR 0014 D10). One decision is open, and the tree is built on its recommended answer:

### 2.1a — What happens to a key deleted from `spec.podLabels` or `spec.podAnnotations`?

ADR 0009 D9 makes every update merge the operator's keys over the live labels and annotations and
keep every other key; its accepted cost is that "a label the operator once set and later stops
setting is never removed by merging". `spec.podLabels` turns that cost into a user-visible one: a
pod label deleted from the `Mosquitto` in Git would stay on the pods forever, and a label a
NetworkPolicy or a scraper selects on keeps granting what it granted.

- **A — The StatefulSet records the keys it applied and removes those that left the spec.**
  *(Recommended, and built.)* Two annotations on the StatefulSet object,
  `mko.gtrfc.com/applied-pod-labels` and `mko.gtrfc.com/applied-pod-annotations`, list the sorted
  keys last written; `builder.MergeStatefulSet` deletes exactly the keys that one lists and the new
  spec does not, then merges. Cost: two annotations of bookkeeping, and a foreign label that
  happens to share a removed key is removed too. It is what `kubectl apply` does with its
  last-applied record, scoped to the two maps the user owns, and it needs no status as memory.
- **B — Accept D9's cost for these keys as well.** No bookkeeping; removing a pod label means
  editing the StatefulSet's pod template by hand, which the next roll keeps. Against R4 of ADR
  0012, half-met: a label reaches the pods, it never leaves.
- **C — Replace the pod template's labels and annotations wholesale, keep merging elsewhere.**
  Simple, and it brings back what D9 removed: `kubectl.kubernetes.io/restartedAt` and every
  policy-engine label on the template disappear on any write.

A is recommended because it is the only option under which a Git change to `spec.podLabels` means
the same thing in both directions, at the cost of state the operator already owns. The answer
becomes an amendment of ADR 0009 D9.

**Answer:** _open_

## Phase 3 — The measurements the user phase stands on

**Goal:** every claim phase 4 builds on is measured against the pinned image, so phase 4 starts on
facts.

**Effort:** S.

| # | Measurement | Rig | Settles |
|---|---|---|---|
| E1 | A `$7$1000$…` line rendered by Go is accepted by the broker, byte format identical to `mosquitto_passwd` | docker | ADR 0014 D3 |
| E2 | The `passwd` and `acl` parsers accept usernames containing `@` and `.` | docker | ADR 0013 D5 |
| E3 | A sidecar as uid `1883`, all capabilities dropped, signals the broker across `shareProcessNamespace` under `enforce=restricted` | Kind | ADR 0014 D4, ADR 0012 D4 |
| E4 | How long the kubelet takes to refresh a changed Secret volume, and whether `tls.crt` and `tls.key` change together | Kind | ADR 0014 D8, ADR 0001 D10 |
| E5 | What the broker does with a mismatched TLS pair at start | docker | ADR 0001 D10 |
| E6 | A 2.0 image fails `--test-config` on the generated file, with file and line | docker | ADR 0007 D10 |
| E7 | The `spec.config` allowlist, taken from `mosquitto.conf(5)` of 2.1.2 | the man page of the pinned version | ADR 0008 D15 |

E1 needs the hashing code of step 4.2 in a minimal form; it is written here and kept. Each row
becomes a section of [broker-behaviour.md](../developer/broker-behaviour.md); each record it
settles says "measured" with the date in its `Status`, or is amended because the measurement said
otherwise. **Done when** all seven sections exist.

## Phase 4 — Users, permissions, and a broker that requires a login

**Goal:** R1, R2, R3 and R5 of ADR 0012 — the core of the release.

**Builds:** [ADR 0013](../adr/0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md)
entire; [ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md)
D1–D4, D6–D8, and D10 for `credentialsSecret`;
[ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
D13–D16; [ADR 0006](../adr/0006-both-install-paths-grant-the-same-authority.md) D9.

**Effort:** L. Built in the order below; each step is a reviewable change that keeps the tree
green.

### 4.1 The `MosquittoUser` kind

- `api/v1/mosquittouser_types.go` (new): `MosquittoUserSpec` with `BrokerRef` (name only — no
  namespace field, ADR 0013 D1), `CredentialsSecret` (`Name`; `UsernameKey` default `username`,
  `PasswordKey` default `password` as `+kubebuilder:default`), `ACLs []MosquittoACL` with `Topic`
  and `Access` (`+kubebuilder:validation:Enum=read;write;readwrite`), a CEL rule on `Topic`
  refusing a leading `$`; `MosquittoUserStatus` with `ObservedGeneration`, `Username`,
  `Conditions`. Printer columns: broker, username, ready.
- `make generate-all`; the chart's CRD template gains the second CRD.
- Tests: integration, in [`crd_validation_test.go`](../../test/integration/crd_validation_test.go)
  — the defaults apply; a `$SYS/#` topic and an unknown access mode are refused at apply.

### 4.2 The renderer (ADR 0013 D3–D6, D8; ADR 0014 D3)

- A new package (proposed `internal/auth`): one function from the broker and its bound users to
  the `passwd` and `acl` content — users sorted by username; the render-time checks (username
  allowlist `^[A-Za-z0-9][A-Za-z0-9._@-]{0,127}$`, the reserved `mko-` prefix case-insensitive,
  `$` topics, oldest-wins collisions by `creationTimestamp` then name); `$7$` hashing,
  PBKDF2-SHA512 at 1000 iterations with a 64-byte salt, and **verify before rehash**: an existing
  hash is kept while the plaintext still verifies against it. The result carries one verdict per
  user for the status.
- The payload is mode-specific behind one interface, so a later dynsec payload is a second
  implementation (ADR 0014 D9).
- Tests: unit — a hundred renders of shuffled input give one output (observed failing without the
  sort); a kept hash for an unchanged password and a new one for a changed password; every refusal;
  the collision rule; a `$` topic refused at render time although CEL was bypassed. E1 moves into
  `test/imagetools` so CI repeats the check of the hash format against the pinned image.

### 4.3 The rendered Secret `<name>-auth` (ADR 0014 D2)

- `internal/builder`: a builder for the Secret (keys `passwd`, `acl`), its name derived in
  `internal/common` (proposed `AuthSecretName`), base labels, written after `ensureOwned` with a
  controller reference like the other four objects. Its content is part of no pod hash.
- The reconciler reads the bound users and their Secrets, renders, compares with the current
  `<name>-auth`, and writes only on a difference.

### 4.4 The broker pod: copy, sidecar, signal (ADR 0014 D4–D6)

- `buildPodSpec`: a volume for `<name>-auth` (no `subPath`), an `emptyDir` for the copy at
  `/mosquitto/auth`, `shareProcessNamespace: true`, an init container (proposed `auth-init`) that
  copies on every start as `1883:1883` mode `0600`, and a sidecar (proposed `reloader`) — both from
  the operator's own image, with the broker container's security context.
- The operator binary gains its second entry point (proposed `manager reload`, in a new package
  proposed `internal/reloader`): compare the mounted bytes, write to a temporary name and rename,
  find the broker process through `/proc`, send SIGHUP. The TLS half follows in phase 5.
- The operator learns its own image from a flag (proposed `--reloader-image`): the chart passes
  its own image string; on the kustomize path a `replacements` entry in `config/default` copies the
  manager's image into the flag — **not verified** that `replacements` can target an element of
  `args`; if it cannot, the kustomize path sets an environment variable instead.
- Tests: unit — the pod spec carries both containers and the volumes, and every container passes
  the restricted check of 2.4; the reloader's copy, rename and change detection against a
  temporary directory. E3 proved the signal.

### 4.5 The generated listener (ADR 0008 D13–D15)

- `GenerateMosquittoConf`: `plugin_load` and `plugin_opt_password_file` /
  `plugin_opt_acl_file` pointing at `/mosquitto/auth/passwd` and `/mosquitto/auth/acl`; the
  listener with `listener_allow_anonymous false`, `use_username_as_clientid true` and `plugin_use`
  for both; `allow_anonymous true` and its comment block removed.
- The `spec.config` allowlist from E7, checked line by line at render time; a refused line gives
  `Ready=False` (proposed reason `ConfigDirectiveRefused`) naming the line, and the reconcile
  writes nothing, so the running configuration stays.
- Tests: unit — the generated file for each shape; `listener`, `connection`, `allow_anonymous` and
  `plugin_load` in `spec.config` refused; an allowed tuning directive passes. The existing
  `TestGenerateMosquittoConf_*` tests rewritten for the new posture.

### 4.6 Watches, status, and the broker without users

- `SetupWithManager`: a field index on `MosquittoUser` by `spec.brokerRef.name`, a `Watches` on
  `MosquittoUser` mapping to its broker, a second index on `spec.credentialsSecret.name` and a
  `Watches` on `Secret` mapping a referenced Secret to the brokers whose users name it, and
  `Owns(&corev1.Secret{})` for `<name>-auth`.
- Each user's status written by the same pass: `observedGeneration`, `username`, one `Ready`
  condition with its reason (proposed: `SecretNotFound`, `KeyNotFound`, `BrokerNotFound`,
  `UsernameInvalid`, `UsernameReserved`, `UsernameConflict`, `TopicRefused`,
  `SecretNotConsumable`). A user's failure never changes the broker's phase. The broker reports how
  many users it accepts; with none, a condition says it accepts nobody.
- The `credentialsSecret` half of `secretSecurity` (2.5) lands here.
- Tests: unit with the fake client for every reason; integration — a user created before its
  Secret becomes `Ready` when the Secret appears, without a manual reconcile.

### 4.7 The Secret grant on both install paths (ADR 0014 D7, ADR 0006 D9)

- RBAC markers: `mosquittousers` `get;list;watch`, `mosquittousers/status` `update`, `secrets`
  `get;list;watch;create;update`; `make generate-all`.
- Chart: proposed values `secretAccess.mode: all` and `secretAccess.namespaces: []`. With `all`,
  the `secrets` rule sits in [`clusterrole.yaml`](../../deploy/helm/mosquitto-operator/templates/clusterrole.yaml);
  with `namespaces`, a Role and RoleBinding per listed namespace and no `secrets` rule in the
  ClusterRole, and the operator gets the list as a flag (proposed `--secret-namespaces`) and
  restricts its Secret cache to it with controller-runtime's per-object cache options — **not
  verified** against `v0.24.1`. A new `NOTES.txt` prints the grant when `all` is active.
- kustomize: `all` in `config/default`; a component for the other mode (proposed
  `config/components/secret-namespaces`).
- `test/rbacparity`: renders and compares both modes; the logged grant count updated.
- README: the grant stated before the install command.

### 4.8 End to end

E2E tests in `test/e2e/` (proposed file `users_test.go`), the first of them being the phase:

- a `MosquittoUser` created against a running broker can publish, and the broker pod did not
  restart;
- a client is refused on a topic outside its ACL;
- a changed password in the user's Secret makes the old password fail and the new one work, with
  no edit to any CR;
- a deleted user's live connection is dropped;
- an anonymous client is refused;
- a second user connecting with another user's client ID does not take over its session;
- a `spec.config` with a second `listener` is refused and the broker keeps serving.

### 4.9 Documentation and records

- README: the `MosquittoUser` reference, the new `Mosquitto` behaviour, the chart values, the
  flags, the generated `mosquitto.conf` example, `<name>-auth` in the naming tables.
- [docs/operations/](../operations/README.md): users and credentials, the grant modes, what
  revocation looks like and how long it takes (E4).
- [docs/security/](../security/README.md): H-1 (anonymous) closed; H-2 and H-14 narrowed by the
  allowlist; the privilege footprint and the credentials page rewritten for the `secrets` grant;
  H-15 for `credentialsSecret`.
- [docs/developer/](../developer/README.md): the renderer, the reloader, the pod, the watches.
- The `Status` and index rows of ADR 0006, 0008, 0013 and 0014.

**Phase 4 is done when** 4.8 passed on CI and every document above describes the built tree.

## Phase 5 — Certificates renewed in place

**Goal:** the TLS half of R3 — a renewal reaches a running broker without a restart.

**Builds:** [ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md) D10,
[ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md)
D5.

**Effort:** S.

- The reloader watches the TLS mount too; before every signal it checks that `tls.crt` and
  `tls.key` form a valid pair with `crypto/tls.X509KeyPair`, and does not signal otherwise.
- `buildPodSpec`: the reloader is present whenever `spec.tls` or a user is configured.
- Tests: unit — a mismatched pair is never signalled. E2E — a cert-manager renewal shows the new
  serial to a fresh `openssl s_client` with no pod restart while a subscriber connected before
  keeps receiving; a Secret edited to a mismatched pair leaves the previous certificate served.
- [rotation.md](../security/rotation.md) and [runtime.md](../operations/runtime.md#a-renewed-certificate):
  "restart" becomes "reload"; H-3 closed.

## Phase 6 — The Home Assistant migration

**Goal:** the release does what ADR 0012 D1 says, on a real cluster — the first observation of
this operator running anywhere but CI.

**Effort:** S, plus the owner's time.

- README: a Flux example — a `Mosquitto`, a `MosquittoUser` and a SOPS-encrypted basic-auth Secret
  per client, the Flux `Kustomization` with health checks on both kinds — and a user-written
  NetworkPolicy against the selector labels (ADR 0008 D16).
- The owner migrates Home Assistant and Zigbee2MQTT onto the new broker through Flux.
- **Done when** both clients run against it, a password rotated in Git reaches both without a
  manual step, and what was run and observed is written into
  [docs/operations/](../operations/README.md).

## Phase 7 — The exporter

**Goal:** broker metrics for Prometheus.

**Builds:** [ADR 0002](../adr/0002-the-metrics-exporter-is-written-here.md) as amended — the
reserved `mko-exporter` user with `read $SYS/#`, its password a key of `<name>-auth`.

**Effort:** M.

- The steps ADR 0002 names; the renderer of 4.2 renders the reserved user; the exporter container
  gets the same security context as every other container.
- Tests: as ADR 0002 names them, plus an E2E test that the exporter logs in as `mko-exporter` and
  that no `MosquittoUser` can claim the name.

## Phase 8 — High availability

**Last, by the owner's decision:** started only when phases 1 to 7 are done
([ADR 0012](../adr/0012-the-first-release-is-one-broker-run-from-git-and-high-availability-is-parked.md)
D3). Its research and its questions are in [ha-research.md](ha-research.md); the questions
HA1–HA7 are put to the owner when this phase starts, not before, and the phase is worked out here
then.

## Later, when a need arrives

| What | Decided in | Built when |
|---|---|---|
| `MosquittoRole` | [ADR 0013](../adr/0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md) D7 | many users share identical ACLs |
| The dynamic-security mode | [ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md) D9 | ACL priorities are needed, or a client with a valid credential must be kickable |

## What is deliberately not planned

- Issuing certificates; cert-manager as a dependency at any layer
  ([ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md)).
- Admission webhooks and conversion webhooks — each needs a serving certificate.
- A NetworkPolicy shipped by the operator ([ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md) D16).
- An image policy in the operator ([ADR 0007](../adr/0007-one-broker-image-pin-and-why-not-the-openssl-tag.md) D9).
- Generating client passwords; the users own their Secrets.
- References across namespaces. A `Mosquitto`, its users, their Secrets and their ACLs share one
  namespace; no reference carries a namespace field
  ([ADR 0013](../adr/0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md) D1).
  The operator itself acts cluster-wide.
