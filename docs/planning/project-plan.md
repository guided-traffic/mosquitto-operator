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

**Goal:** every tracked statement matches the code, the E2E tier gates releases again, and the
pipeline holds no authority it does not use — before anything new is built on top.

**Effort:** M.

### 1.1 Restore the E2E tier in CI

- [`release.yml`](../../.github/workflows/release.yml): uncomment the block under
  `TEMPORARILY DISABLED` (the two legs of `e2e-tests` and the `e2e-gate` job) and add `e2e-tests`
  back to the `needs:` of `semantic-release`, where the comment `TEMPORARILY REMOVED` marks it.
- [ADR 0004](../adr/0004-two-e2e-legs-and-no-version-matrix.md) `Status`: that the tier was
  disabled from 2026-09-01 until the restoring change, and why (the comment in the workflow); the
  index row back to *Implemented*.
- [ADR 0005](../adr/0005-fork-pull-requests-execute-on-the-self-hosted-runners.md): D4's list of
  jobs that hold a credential and the Context's job count corrected to the running jobs.
- [`release-template.hbs`](../../.github/release-template.hbs) lines 28–31: the gating sentence is
  true again; `make test-release-tooling` renders it.
- **Done when** both legs ran green on a pull request and the multi-node leg's guard grep found
  `--- PASS: TestE2E_AntiAffinity_HardSpreadsAcrossNodes`; the run is named in ADR 0004's
  `Status`. A leg that fails for an infrastructure reason is recorded there with its cause, not
  worked around.

### 1.2 Correct what the code contradicts

Each line: the text corrected to the tree, checked against it once more in the change.

| Where | Correction |
|---|---|
| [ADR 0003](../adr/0003-the-go-version-is-one-fact-in-four-files.md) `Status` | the four sites read `1.27.1` today; the record names the fact, not a value Renovate moves |
| [`cmd/main.go`](../../cmd/main.go), comment of `bindZapFlags` | nothing in the deployment passes `--zap-log-level`; `make run` does |
| [`internal/common/labels.go`](../../internal/common/labels.go), comment of `sanitizeLabelValue` | the test is `TestExtractVersionFromImage_AlwaysProducesAValidLabel` |
| `internal/common/labels.go` lines 47–48 | `spec.image` carries no `MinLength` in the CRD |
| [`clusterrole.yaml`](../../deploy/helm/mosquitto-operator/templates/clusterrole.yaml), header comment | `test/rbacparity` compares it with `config/rbac/role.yaml`; it holds no leases rule — that is the namespaced Role of `leader-election.yaml` |
| [`values.yaml`](../../deploy/helm/mosquitto-operator/values.yaml), `leaderElection` comment | the leases rule is in the namespaced Role, not the ClusterRole |
| [`Chart.yaml`](../../deploy/helm/mosquitto-operator/Chart.yaml), [`package.json`](../../package.json), [`Containerfile`](../../Containerfile), [`build.yml`](../../.github/workflows/build.yml) | "provisioning highly available Mosquitto MQTT brokers" replaced by the README's pitch, in all four in one change |
| `build.yml`, the buildx cache comment | releases `v0.1.0`–`v0.1.8` exist; whether `build.yml` built them is checked on GitHub and written as found |
| ADR 0005 D5 and D8, and the comment in `release.yml` before `npm ci` | Renovate does not run on push; the app token is in the Release step's environment only — the protection of `--ignore-scripts` is real, its stated reason is not |
| [ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md), Consequences | a missing TLS Secret holds the pod in `ContainerCreating` rather than `CrashLoopBackOff` — observed on Kind first (`make e2e-local`, a `Mosquitto` naming a Secret that does not exist), then written as observed |

### 1.3 Take back the pipeline's unused authority

- `semantic-release`: `permissions:` reduced to what checkout needs; every write already goes
  through the GitHub App token. `build.yml`: a top-level `permissions: contents: read`, widened per
  job.
- `build`: `docker logout` right after the push, before `anchore/sbom-action` and
  `softprops/action-gh-release` run.
- The third-party actions referenced by tag — `anchore/sbom-action@v0`,
  `softprops/action-gh-release@v3`, `marocchino/sticky-pull-request-comment@v3`,
  `renovatebot/github-action@v46.3.6` — pinned by commit SHA with a Renovate digest manager; the
  exception comment in `release.yml` and the security page updated to the truth.
- `moby/buildkit:v0.12.0` in `build.yml`: a Renovate `customManager` for the `image=` line, guarded
  by `make verify-ci-references`, or the pin dropped. Before that, the version is checked against
  the BuildKit advisories of 2024 (CVE-2024-23651, -23652, -23653); not checked yet.
- Renovate automerge for images ([`renovate.json`](../../renovate.json) lines 128–139 and
  217–228): minor updates of the broker image need review, because the operator ships it as the
  default into every cluster.
- [ci-and-supply-chain.md](../security/ci-and-supply-chain.md): H-9 and H-10 rewritten to what is
  left.
- **Done when** a release run on `main` succeeds with the reduced permissions, and
  `make verify-ci-references` was observed failing once with a broken BuildKit regex.

### 1.4 Pin the ownership refusal message

- A unit test in [`mosquitto_controller_test.go`](../../internal/controller/mosquitto_controller_test.go)
  asserts the exact message of `ensureOwned` for ConfigMap, Service and StatefulSet
  ([ADR 0009](../adr/0009-delete-only-through-owner-references.md) D5), observed failing once
  against an edited format string.

## Phase 2 — Pod labels, the configuration gate, the admission guard, and the first Secret switch

**Goal:** R4 and R6 of ADR 0012, the label rule of ADR 0009 D9, and the TLS half of the Secret
switch — everything that does not need users.

**Builds:** [ADR 0012](../adr/0012-the-first-release-is-one-broker-run-from-git-and-high-availability-is-parked.md)
D4, D5; [ADR 0007](../adr/0007-one-broker-image-pin-and-why-not-the-openssl-tag.md) D9, D10;
[ADR 0009](../adr/0009-delete-only-through-owner-references.md) D9;
[ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md)
D10 for the TLS Secret; [ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md)
D1 and D6 as amended.

**Effort:** M.

### 2.1 `spec.podLabels` and `spec.podAnnotations` (ADR 0012 D5)

- [`api/v1/mosquitto_types.go`](../../api/v1/mosquitto_types.go): two optional
  `map[string]string` fields on `MosquittoSpec`, documented as "merged under the operator's own
  keys".
- [`internal/builder/statefulset.go`](../../internal/builder/statefulset.go) `BuildStatefulSet`:
  the template labels are `podLabels` with `common.BaseLabels` written over them; the template
  annotations are `podAnnotations` with `AnnotationPodSpecHash` and `AnnotationConfigHash` written
  over them. `StatefulSetHasChanged` already compares template labels through
  `MapEntriesMissing`; the user's annotation keys join that comparison.
- `make generate-all`; the chart's CRD copy follows through `sync-helm-crd`.
- Tests: unit — a `podLabels` entry for `app.kubernetes.io/instance` and a `podAnnotations` entry
  for `mko.gtrfc.com/config-hash` lose to the operator's values; a new label makes
  `StatefulSetHasChanged` true. E2E — a label added to the CR appears on the pods after the roll.

### 2.2 Updates keep what others added (ADR 0009 D9)

- [`internal/controller/mosquitto_controller.go`](../../internal/controller/mosquitto_controller.go)
  `reconcileConfigMap`, `reconcileService`, `reconcileStatefulSet`: `current.Labels =
  desired.Labels` becomes a merge (a helper beside `MapEntriesMissing` in
  `internal/common/labels.go`, proposed `MergeLabels`), and the template write merges labels and
  annotations into `current.Spec.Template` before its spec is replaced.
- Tests: unit — a foreign label on each of the three kinds survives an update made for another
  reason; a foreign template label and `kubectl.kubernetes.io/restartedAt` survive a replica
  change; observed failing against today's assignments.
- [runtime.md](../operations/runtime.md#what-a-pass-corrects-and-what-it-leaves-alone): the
  paragraph on `kubectl rollout restart` rewritten.

### 2.3 The `--test-config` init container (ADR 0007 D10)

- `buildPodSpec`: an init container (proposed name `config-check`) from `ResolveImage(m)`, running
  `/usr/sbin/mosquitto -c /mosquitto/config/mosquitto.conf --test-config`, with the `config` and
  `data` mounts and the broker container's security context. It is part of the pod spec, so the
  pod-spec hash covers it.
- The `image` field description and the README state the supported line, 2.1.x.
- Tests: unit — the init container exists with the broker container's security context. E2E — a
  `spec.config` typo leaves the broker container unstarted, and the init container's log carries
  the broker's message with file and line.

### 2.4 The restricted-admission guard (ADR 0012 D4)

- First settle the open fact: does envtest's API server enforce `pod-security.kubernetes.io/enforce`
  labels? If it does, an integration test creates a namespace with `enforce=restricted` and a Pod
  from `BuildStatefulSet(m).Spec.Template` for each shape — plain, TLS, storage, both, hard
  anti-affinity; if it does not, the same test runs in the E2E tier.
- Observed failing once with `allowPrivilegeEscalation: true` on the broker container, the API
  server's own message recorded.

### 2.5 `secretSecurity` for the TLS Secret (ADR 0014 D10)

- [`cmd/main.go`](../../cmd/main.go) `bindOperatorFlags`: a flag (proposed `--secret-security`,
  bool, default `false`) on `operatorFlags`, passed to `MosquittoReconciler`.
- Chart: value `secretSecurity: false`, passed as the flag in
  [`deployment.yaml`](../../deploy/helm/mosquitto-operator/templates/deployment.yaml); kustomize:
  the flag in [`manager.yaml`](../../config/manager/manager.yaml), same default.
- With `true` the reconciler reads the TLS Secret's metadata — labels only, never data — and
  refuses one without the label (proposed `mko.gtrfc.com/consumable: "true"`): `Ready=False`,
  proposed reason `SecretNotConsumable`, the StatefulSet left as it is. That read needs `get` on
  `secrets`, which the tree does not grant before phase 4: phase 2 adds a read-only `secrets` rule
  **only while `secretSecurity` is `true`**, rendered under the same condition on both install
  paths, and `test/rbacparity` compares both settings. Phase 4's grant supersedes it.
- Tests: unit — refused without the label, accepted with it, accepted without it when `false`;
  observed failing with the check removed. Integration — the flag reaches the operator on both
  paths.
- README: the value, the flag, the label, and the trust rule of `false` next to the install
  command; [trust-boundaries.md](../security/trust-boundaries.md#h-15): H-15 names the switch as
  its mitigation.

**Phase 2 is done when** every test above passed on CI, the E2E legs included; the README
reference covers `podLabels`, `podAnnotations`, `secretSecurity` and the flag; the records'
`Status` and index rows say what is built.

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
