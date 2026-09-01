# Mosquitto Operator

Repo: https://github.com/guided-traffic/mosquitto-operator
Module `github.com/guided-traffic/mosquitto-operator` · API group `mko.gtrfc.com`, version `v1` ·
Kind `Mosquitto`, resource `mosquittoes`, short name `mq`.

**Nothing in this repository has ever been observed running against a real cluster.** Every
statement below is read out of the tree; the E2E suite exists and is wired into CI, but no run of
it has been seen. Where that matters, the ADRs say so in their own `Status` sections.

## Language policy

All code, comments, commit messages, documentation and CRD fields are written in **English**.

## Architecture Decision Records

Durable decisions live in [`docs/adr/`](docs/adr), one file per decision family, named
`NNNN-kebab-case-title.md`, indexed by [`docs/adr/README.md`](docs/adr/README.md) — a new ADR is
added to its Index table in the same change that adds the file. Read the relevant ADR before changing the
behaviour it describes, and update it **in the same change** — an ADR that describes the old rule
as current is worse than no ADR. A re-decision states the new rule in `Decision`, records the
amendment in `Status` with its date, and marks the superseded rule in place rather than deleting it.

| ADR | Decision |
|---|---|
| [0001](docs/adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md) | The operator consumes TLS material, it never issues it |
| [0002](docs/adr/0002-the-metrics-exporter-is-written-here.md) | The broker metrics exporter is written in this repository — **nothing in it is implemented** |
| [0003](docs/adr/0003-the-go-version-is-one-fact-in-four-files.md) | The Go version is one fact in four files, and one Renovate PR moves all four |
| [0004](docs/adr/0004-two-e2e-legs-and-no-version-matrix.md) | Two E2E legs, shaped by node count, and no version matrix |
| [0005](docs/adr/0005-fork-pull-requests-execute-on-the-self-hosted-runners.md) | Fork pull requests execute on the self-hosted runners, gated outside the repository |
| [0006](docs/adr/0006-both-install-paths-grant-the-same-authority.md) | Both install paths grant the same authority, and a test compares what they render |
| [0007](docs/adr/0007-one-broker-image-pin-and-why-not-the-openssl-tag.md) | One broker image pin, on `2.1.2-alpine`, and not on the `-openssl` tag |
| [0008](docs/adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md) | The generated broker is anonymous, and `spec.config` can undo the rest |
| [0009](docs/adr/0009-delete-only-through-owner-references.md) | Delete only through owner references, and never patch |
| [0010](docs/adr/0010-a-check-is-not-a-check-until-it-has-failed-on-purpose.md) | A check is not a check until it has failed on purpose |

## What this operator does — and what it is not

One `Mosquitto` produces exactly four objects: a ConfigMap holding the generated
`mosquitto.conf`, a headless Service, a ClusterIP client Service and a StatefulSet of broker pods.
That is the whole managed set
([`internal/controller/mosquitto_controller.go`](internal/controller/mosquitto_controller.go),
`reconcileResources`).

**The broker pods are independent Mosquitto processes behind one Service.** No bridging, no shared
sessions, no shared retained messages, no clustering. Raising `spec.replicas` buys process
redundancy, not a highly available broker — a subscriber on one pod never sees a retained message
published through another. Highly available brokers are the project goal; this version does not
deliver them. Do not write a comment, doc line or commit message that implies otherwise.

**Not in the tree, and not to be documented as if it were:** the metrics exporter (`cmd/exporter`
does not exist — the decision is recorded in [ADR 0002](docs/adr/0002-the-metrics-exporter-is-written-here.md)
and nothing in it is built), authentication, ACLs, PodDisruptionBudgets, NetworkPolicies, admission
webhooks, ServiceMonitor, PrometheusRule, and any cert-manager dependency at any layer. The chart
has no templates for any of them.

## The CRD

One CRD, one Kind. Every field of `MosquittoSpec`, populated:

```yaml
apiVersion: mko.gtrfc.com/v1
kind: Mosquitto
metadata:
  name: broker
  namespace: messaging
spec:
  replicas: 3                          # default 1, Minimum=1, Maximum=9
  image: eclipse-mosquitto:2.1.2-alpine # optional; empty uses builder.DefaultImage (same value)
  antiAffinity: hard                   # default "off"; Enum=off;soft;hard
  config: |                            # optional; appended to the generated file VERBATIM, unvalidated
    max_keepalive 120
  tls:
    secretName: broker-tls             # required inside the block, MinLength=1
  storage:
    size: 1Gi                          # required inside the block, MinLength=1
    storageClassName: fast             # optional; empty/absent uses the cluster default
  resources:                           # optional; corev1.ResourceRequirements, passed through
    requests:
      cpu: 100m
      memory: 128Mi
    limits:
      cpu: 500m
      memory: 512Mi
status:
  phase: Ready                         # Pending | Progressing | Ready | Failed
  readyReplicas: 3                     # mirrors sts.status.readyReplicas
  observedGeneration: 4                # the .metadata.generation the last pass acted on
  conditions:                          # only one type exists: "Ready"
    - type: Ready
      status: "True"
      reason: AllReplicasReady
      message: 3/3 broker pods are ready
```

What each field actually does ([`api/v1/mosquitto_types.go`](api/v1/mosquitto_types.go),
[`internal/builder`](internal/builder)):

| Field | Effect |
|---|---|
| `replicas` | `spec.replicas` of the StatefulSet. A change moves the replica count only; it does not touch the pod template, so it rolls nothing. |
| `image` | The broker container image. Empty resolves to `builder.DefaultImage`. Also feeds the `app.kubernetes.io/version` label through `ExtractVersionFromImage`, whose contract is that it **always returns a valid label value**: a digest becomes its 12-char hex prefix, anything else is sanitised to `[A-Za-z0-9._-]`, truncated to 63 bytes and trimmed, and an empty result becomes `unknown`. That contract is asserted against apimachinery's `validation.IsValidLabelValue` in [`internal/common/labels_test.go`](internal/common/labels_test.go), not against expected strings — because the defect this replaced was a test pinning `sha256:abc123` as intended, a value the API server refuses, which made every owned write fail for a digest-pinned image. |
| `config` | Appended after everything the operator generates, unparsed. A repeated global option therefore **wins** over the generated one, and a `listener` line here adds a listener the operator neither models nor exposes as a port. A rejected file is a CrashLoopBackOff, never a rejected resource. |
| `antiAffinity` | `off` renders no affinity block at all (nil); `soft` renders one `preferred…` term at weight `100`; `hard` renders one `required…` term. Topology key is always `kubernetes.io/hostname`, and the term selects only the pods of this same `Mosquitto`. |
| `tls.secretName` | Mounts that existing Secret read-only at `/mosquitto/tls` and **moves** the single listener from 1883 to 8883. The operator never creates, renews or watches it. |
| `storage` | Renders a `volumeClaimTemplates` entry named `data`. Absent means an `emptyDir` under the same name, so the mount path is identical either way. |
| `resources` | Set verbatim on the broker container. |

Status is written by `updateStatus`/`setPhase`: `phase` and the single `Ready` condition are
always written together, so they can never disagree about which generation they describe.
`Failed` describes the **operator**, not the brokers — a write the operator could not perform.
Pods that were already running keep running.

Two spec shapes are half-filled-safe by helper, not by validation: `IsTLSEnabled()` is false for an
empty `secretName` and `IsStorageEnabled()` is false for an empty `size`, and
`AntiAffinityMode()` treats any unknown value as `off` (the weakest setting) in case the enum is
ever bypassed.

## Deterministic names the operator produces

For a `Mosquitto` named `<name>`. Sources:
[`internal/common/labels.go`](internal/common/labels.go),
[`internal/builder/configmap.go`](internal/builder/configmap.go),
[`internal/builder/statefulset.go`](internal/builder/statefulset.go).

| Thing | Value |
|---|---|
| StatefulSet | `<name>` |
| Client Service (ClusterIP) | `<name>` |
| Headless Service | `<name>-headless` (`publishNotReadyAddresses: true`) |
| ConfigMap | `<name>-config`, single key `mosquitto.conf` |
| PVC template / data volume | `data` (immutable once created — never renamed) |
| Broker container | `mosquitto`, command `/usr/sbin/mosquitto -c /mosquitto/config/mosquitto.conf` |
| Volumes | `config`, `tls` (TLS only), `data` |
| Mount paths | `/mosquitto/config`, `/mosquitto/tls`, `/mosquitto/data` |
| Ports | `1883` name `mqtt`, or `8883` name `mqtts` — exactly one, never both |
| Base labels | `app.kubernetes.io/name=mosquitto`, `/instance=<name>`, `/managed-by=mosquitto-operator`, `/component=broker`, `/version=<image tag>` |
| Selector labels | the first three only — the version label is deliberately excluded, or a Service would stop matching its pods on an image change |
| Pod-template annotations | `mko.gtrfc.com/pod-spec-hash`, `mko.gtrfc.com/config-hash` |
| Anti-affinity topology key | `kubernetes.io/hostname`, soft weight `100` |
| Broker uid/gid/fsGroup | `1883` |
| Leader election Lease ID | `mosquitto-operator.mko.gtrfc.com` |
| Generated ClusterRole (kustomize) | `mosquitto-operator-role` |
| Operator ports | `:8080` metrics, `:8081` health probes |

Every managed name is derived from the CR name, and whoever may create a `Mosquitto` in a namespace
picks that name — which is why every write is preceded by an ownership check (below).

## Reconcile rules that are easy to break

- **`ensureOwned` before every write onto a generated name.** `metav1.IsControlledBy` or refuse; a
  label is not a proof. The refusal is a reconcile failure (`phase: Failed`, `Ready=False`,
  reason `ReconcileFailed`) because the resource cannot do its job without the object. A new
  managed kind inherits this, not an exemption. → [ADR 0009](docs/adr/0009-delete-only-through-owner-references.md)
- **No `delete` and no `patch` in the ClusterRole** — that is the reconciler's entire grant, and
  it is the same in both install paths. Teardown is the garbage collector's job through the
  controller references, and a `Mosquitto` carrying a `DeletionTimestamp` gets **no writes at
  all** — writing again would race the collection. The namespaced leader-election `Role` does hold
  `delete`/`patch` on its own `Lease` and `patch` on `Events`: that is client-go's `LeaseLock`,
  belongs to the manager rather than the reconciler, and is scoped to the release namespace.
- **`StatefulSetHasChanged` compares replicas, object labels, template labels and the two hash
  annotations — never the pod spec structurally.** The API server defaults a long list of pod
  fields, so a structural comparison would report drift on every pass and loop forever. The
  pod-spec hash covers the whole built pod spec, so a new pod-template field is picked up
  automatically; a new field **outside** the template is not.
- **`volumeClaimTemplates` are written on create and never updated.** They are immutable; a changed
  `spec.storage` does not converge and needs the StatefulSet recreated by hand.
- **Config changes need the config hash.** Mosquitto reads its file once at startup and a ConfigMap
  update restarts nothing, so `mko.gtrfc.com/config-hash` on the pod template is what carries a
  config change into a roll.
- Watches: `For(&Mosquitto{})` with `GenerationChangedPredicate` plus `Owns` on StatefulSet,
  ConfigMap and Service. Status-only writes therefore do not wake the controller; readiness reaches
  status through the `Owns` watch.
- Concurrency: `--max-concurrent-reconciles`, default `DefaultMaxConcurrentReconciles = 4`, chart
  value `maxConcurrentReconciles`. Passes for the *same* resource stay serialised at any value.

## The generated `mosquitto.conf`, and the posture that comes with it

`GenerateMosquittoConf` emits logging to stdout, `persistence true` under `/mosquitto/data/`,
exactly one listener, `allow_anonymous true`, and then `spec.config` verbatim.

**The generated broker accepts anonymous clients, always.** Mosquitto 2.x rejects every client on a
listener with no configured authentication, this API models none, so the file opts in explicitly —
without it the default resource would serve nobody. `spec.tls` encrypts the listener and
authenticates the *broker* to its clients; it authenticates no client to the broker, because
nothing sets `require_certificate`. Until the API models authentication, `spec.config` is where it
goes, and on the pinned 2.1 image that means the **`password-file` and `acl-file` plugins** — the
`password_file` and `acl_file` options are deprecated in 2.1 and removed in 3.0, so generating them
would be a migration to run later with users attached.
→ [ADR 0008](docs/adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)

TLS material reaches a broker exactly one way: `spec.tls.secretName` names an existing Secret
carrying `tls.crt` and `tls.key` in the CR's namespace. Two ways of filling it are first class and
**neither is this project** — `kubectl create secret tls`, or a cert-manager `Certificate` the
administrator owns. Nothing this repository ships depends on cert-manager; the E2E job installs it
only to exercise the second path against a real issuer (`make cert-manager-install`), and the
suite writes the `Certificate` through the dynamic client so no cert-manager Go dependency enters
the module graph. **The operator does not watch that Secret**: a rotation reaches running pods only
when they restart. → [ADR 0001](docs/adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md)

## Testing

Five tiers, separated by build tag. A tier answers what no cheaper tier can.

| Tier | Build tag | Needs | What only it can answer |
|---|---|---|---|
| Unit | *(none)* | nothing | Builder output and reconcile logic against `controller-runtime`'s fake client. No control plane is started anywhere in this tier. |
| Integration | `integration` | envtest | What a **real API server** decides: CRD defaulting and validation, whether the built objects are accepted, whether the owner references are accepted as written. envtest runs no kubelet and no kube-controller-manager, so **no pod starts and no garbage collection happens here**. |
| E2E | `e2e` | Kind + Helm-installed operator | A running broker: that the listener speaks MQTT and not merely TCP (`mosquitto_pub`/`mosquitto_sub` inside the pod), that hard anti-affinity really spreads, that cert-manager material really serves MQTTS, and that deleting the CR really collects the owned objects. The suite imports nothing from `internal/` on purpose — it addresses names, labels and ports as literals, the way a user does. |
| Image tools | `imagetools` | docker, no cluster | Whether the pinned image actually contains the binaries this repo executes inside it. |
| RBAC parity | `rbacparity` | helm + kustomize, no cluster | Whether the two install paths grant the same authority. |

**No `-short`, and no `testing.Short()` gate.** `make test-unit` deliberately passes no `-short`,
so no test can silently remove itself from CI while still passing locally.

### The Makefile is the entry point

Always run tests, linting and analysis through a Make target. CI enters through the same targets,
which is what keeps a local result and a CI result comparable.

| Task | Target |
|---|---|
| Unit tests | `make test-unit` |
| Unit tests with coverage | `make test-unit-coverage` |
| Integration tests | `make test-integration` |
| Integration tests with coverage | `make test-integration-coverage` |
| E2E (against a running Kind cluster) | `make test-e2e` (`E2E_RUN=` filters by test name) |
| Full local E2E, cluster included | `make e2e-local` |
| Pinned-image tool check | `make test-image-tools` |
| RBAC parity of both install paths | `make verify-rbac-parity` |
| Renovate customManagers still match | `make verify-ci-references` |
| Release notes still render | `make test-release-tooling` |
| Lint / auto-fix | `make lint` / `make lint-fix` |
| Security scan | `make gosec` |
| Vulnerabilities | `make vuln` |
| Cyclomatic complexity (threshold 15) | `make cyclo`, report `make cyclo-report` |
| Regenerate CRD, RBAC, DeepCopy, chart CRD | `make generate-all` |
| Build binary / image | `make build` / `make docker-build` |

Tools resolve to `$(LOCALBIN)` (`./bin`), never to `PATH` — a tool picked up from `PATH` silently
ignores the pin next to it.

`make test-unit` and `make test-unit-coverage` still declare `envtest` as a prerequisite and export
`KUBEBUILDER_ASSETS`, even though no unit test starts a control plane. Do not conclude from that
env var that the unit tier needs a cluster.

### The E2E matrix is two legs, and the axis is node count

| Leg | Workers | Cluster | Filter |
|---|---|---|---|
| `single-node` | 0 | `mosquitto-operator-test` | none — full suite |
| `multi-node` | 3 | `mosquitto-operator-test-multinode` | `TestE2E_AntiAffinity` |

Three workers, not two: Kind keeps the control-plane `NoSchedule` taint on multi-node clusters, so
spreading three replicas needs three schedulable workers. The multi-node leg sets
`E2E_REQUIRE_MULTI_NODE=true`, which turns "fewer than 3 schedulable nodes" from a skip into a
failure, and it greps its own log for `--- PASS: TestE2E_AntiAffinity_HardSpreadsAcrossNodes` so a
filter that matched nothing cannot pass as green. There is deliberately **no version-line matrix**;
the escape hatch is the `E2E_MOSQUITTO_IMAGE` variable. The `e2e-gate` job carries the stable
status context `E2E Tests` so the required check never points at a matrix leg, whose contexts read
`E2E Tests (<topology>)` and are renamed whenever a leg is. Reproduce a leg locally with
`KIND_WORKERS`.
→ [ADR 0004](docs/adr/0004-two-e2e-legs-and-no-version-matrix.md)

## Two install paths, and they must grant the same authority

Helm (`deploy/helm/mosquitto-operator`) and kustomize (`kustomize build config/default`). Only the
kustomize path is generated: `make generate-all` runs controller-gen over the kubebuilder markers in
[`internal/controller/mosquitto_controller.go`](internal/controller/mosquitto_controller.go) and
writes [`config/rbac/role.yaml`](config/rbac/role.yaml). The chart's
[`clusterrole.yaml`](deploy/helm/mosquitto-operator/templates/clusterrole.yaml) is hand-written and
nothing regenerates it.

**A new RBAC marker updates the chart template in the same change.** Both failure directions are
silent otherwise — too few verbs and only chart users 403 on every pass, too many and the chart
hands out cluster-wide authority the code never asked for; neither has a compilation step to break.
[`test/rbacparity`](test/rbacparity/rbac_parity_test.go) renders both paths, decodes them into
`rbacv1` types and compares `(kind, apiGroup, resource) -> sorted verbs`, so rule order and grouping
wash out. Leader election is the one intended difference in *shape*, not in authority: a namespaced
`Role` on **both** paths, never in the ClusterRole. The rendered authority is **8 grants**, the
count the test logs — verified by running `make verify-rbac-parity` on 2026-09-01; the grant table
itself is D6 of the ADR below.

Parity is about **authority**, not about the whole install, and two asymmetries are recorded rather
than fixed (both in that ADR's residual risks):

- [`config/manager/manager.yaml`](config/manager/manager.yaml) passes `--leader-elect`
  unconditionally, while the chart passes it only under `leaderElection.enabled`. The RBAC matches
  either way; the behaviour does not, and no test compares behaviour.
- `make install` applies [`config/rbac`](config/rbac) **without** the `config/default` overlay, so
  those objects carry no `mosquitto-operator-` name prefix and the `Role`/`RoleBinding` land in the
  literal namespace `system`. The parity test only ever renders `config/default`, so that rendering
  is checked by nothing.
→ [ADR 0006](docs/adr/0006-both-install-paths-grant-the-same-authority.md)

## The broker image pin lives in two places, held equal by a test

`eclipse-mosquitto:2.1.2-alpine`, in exactly two constants:

- `builder.DefaultImage` ([`internal/builder/statefulset.go`](internal/builder/statefulset.go)) — what a CR that names no image gets.
- `testimages.MosquittoImage` ([`test/testimages/images.go`](test/testimages/images.go)) — what the suites provision.

`TestPinnedImageIsTheOperatorDefault` asserts the two are equal, because otherwise the image-tools
job would check an image no broker actually runs. One Renovate `customManager` matches both files
from their `// renovate:` comments so they move in one PR, and a packageRule caps the pin at
`<3`. **Do not copy the pin anywhere else** — not into the Makefile, not into a workflow.

No `-openssl` tag, anywhere: it resolves to the same digest as the plain tag, and the default image
already links OpenSSL 3. 2.1 rather than 2.0 so the config generator never starts out emitting
options that 3.0 removes. → [ADR 0007](docs/adr/0007-one-broker-image-pin-and-why-not-the-openssl-tag.md)

## The Go version is one fact in four files

`1.27.0` appears in [`go.mod`](go.mod), [`Containerfile`](Containerfile) (`golang:1.27.0-alpine`)
and as `GO_VERSION` in [`.github/workflows/build.yml`](.github/workflows/build.yml) and
[`.github/workflows/release.yml`](.github/workflows/release.yml); the fourth is `go-1.27-blue` in
[`.github/release-template.hbs`](.github/release-template.hbs) (major.minor only, which is why that
manager uses `loose` versioning and not `semver`). All four have a Renovate `customManager` and
share `groupName: "Go version"`, so one PR moves all four. The workflow manager's pattern also
selects [`.github/workflows/renovate.yml`](.github/workflows/renovate.yml), which carries no
`GO_VERSION`; that is a deliberately tolerated zero, and the guard prints
`.github/workflows/renovate.yml (0)` so it stays visible.

A `customManager` whose regex matches nothing fails **silently** — Renovate reports no error, the
manager just contributes nothing and its file drops out of the group. `make verify-ci-references`
([`hack/verify-ci-references.mjs`](hack/verify-ci-references.mjs)) proves every manager still selects
a real file and matches inside it, and fails on zero selected files, on a `matchString` that
matches nowhere across them, and on a match that captures no `currentValue`. **A new place that
names the Go version arrives with its own customManager, in the same change.** → [ADR 0003](docs/adr/0003-the-go-version-is-one-fact-in-four-files.md)

## The checks that exist

All in [`.github/workflows/release.yml`](.github/workflows/release.yml) unless noted; every job runs
on `self-hosted`.

| Check | Entry point | Job |
|---|---|---|
| Lint (`go vet`, `gofmt -l`, golangci-lint) | `make lint` | `linter` |
| gosec | `make gosec` | `gosec` |
| govulncheck | `make vuln` | `govulncheck` |
| gocyclo over threshold 15 | `make cyclo` | `cyclomatic-complexity` |
| Generated artifacts committed | `make generate-all` + dirty-tree check | `generated-manifests` |
| RBAC parity | `make verify-rbac-parity` | last step of `generated-manifests` |
| Renovate customManagers | `make verify-ci-references` | first step of `release-tooling` |
| Release notes render | `node hack/verify-release-tooling.mjs` | `release-tooling` |
| Unit + integration coverage, badge, PR comment | `make test-unit-coverage`, `make test-integration-coverage` | `unit-tests`, `integration-tests`, `coverage-report` |
| E2E, two legs | `make test-e2e` | `e2e-tests` → `e2e-gate` |
| Pinned image contains what we execute | `make test-image-tools` | `mosquitto-image-tools` |
| ClamAV source scan, Trivy container scan | in-workflow | `malware-scan`, `container-malware-scan` |

`semantic-release` runs only on a push to `main` and lists thirteen jobs in its `needs` — every job
above except the `e2e-gate` aggregator itself, which it bypasses by depending on `e2e-tests`
directly.
Note one deliberate asymmetry: the local target `make test-release-tooling` runs
`npm ci --no-audit --no-fund`, while the CI job runs `npm ci --ignore-scripts`, then
`npm audit signatures`, then the script directly — the job holds no secret at that point, but the
`semantic-release` job below it holds `BOT_PAT`, which is why lifecycle scripts stay off.

**A check is not a check until it has failed on purpose.** A new guard is trusted only after it has
been observed failing against a deliberately broken tree — and the break must be *the* failure the
guard exists to catch, not any failure. Record the observation with the exact message the guard
emitted. Assertions go on strings only the guarded artefact can produce, and every guard asserts
that it read something at all, so an empty render cannot pass. Guards run without a cluster, which
is what lets them run on the pull request.
→ [ADR 0010](docs/adr/0010-a-check-is-not-a-check-until-it-has-failed-on-purpose.md)

## Security posture, in one paragraph

The operator holds a **cluster-wide** ClusterRole: `get;list;watch;create;update` on `configmaps`,
`services` and `statefulsets` in every namespace, `get;list;watch` on `mosquittoes`, `update` on
`mosquittoes/status` and `/finalizers` — no `delete`, no `patch`, and **no rule for `secrets`**,
because the kubelet mounts the TLS material, not the operator. Broker pods run non-root as uid/gid
`1883` with `fsGroup` 1883, a read-only root filesystem, all capabilities dropped, the
`RuntimeDefault` seccomp profile and no ServiceAccount token, which satisfies the restricted Pod
Security Standard. The exposed risk is on the data plane, not the control plane: the generated
broker is anonymous, TLS authenticates the broker and not its clients, anyone who can create or
update a `Mosquitto` in a namespace controls that broker's entire configuration through
`spec.config`, the operator's own `:8080/metrics` endpoint is plain HTTP with no authentication
wherever it binds, no NetworkPolicy is shipped, and fork pull requests execute on the self-hosted
runners (an accepted risk, gated by a GitHub setting rather than by a workflow guard; secrets are
not exposed to fork runs, so the exposure is code execution, not disclosure). The full trust
boundaries, the privilege footprint and the hardening checklist are in
[`SECURITY_ARCHITECTURE.md`](SECURITY_ARCHITECTURE.md); the fork decision is
[ADR 0005](docs/adr/0005-fork-pull-requests-execute-on-the-self-hosted-runners.md).

# Working rules

- Keep cyclomatic complexity under 15 for every function; `make cyclo` is the gate.
- Run `make lint` and the relevant test target before reporting a task done.
- Write tests for every feature and fix, in the cheapest tier that can actually answer the
  question — and never in a tier that cannot (envtest starts no pod and collects no garbage).
- Run `make generate-all` after any change under `api/v1/` or to an RBAC marker, and commit the
  result: `generated-manifests` fails on a dirty tree.
- **Do not commit, add or push to git.** Report a conventional commit message and let the user
  review and commit.
- Temporary files go into a local `tmp/` folder in this repository, never into the system `/tmp`.
- Architecture decisions belong in [`docs/adr/`](docs/adr), not here. This file carries
  project-wide working rules and short pointers; the reasoning, the alternatives and the residual
  risks live in the ADR.
- Separate verified from unverified in every report, and never let an assumption travel as a fact.
  Nothing here has run against a real cluster — say so where it matters.
- User-facing documentation: [`README.md`](README.md), [`DEVELOPER.md`](DEVELOPER.md),
  [`SECURITY_ARCHITECTURE.md`](SECURITY_ARCHITECTURE.md).
