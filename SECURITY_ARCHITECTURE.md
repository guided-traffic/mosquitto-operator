# Security Architecture

What the Mosquitto operator is trusted with, what it is allowed to do, where
credentials live, and — the longer half — what the isolation it provides does
**not** cover.

Every statement below was read out of this repository: the reconciler and its
`+kubebuilder:rbac` markers
([`internal/controller/mosquitto_controller.go`](internal/controller/mosquitto_controller.go)),
the object builders ([`internal/builder/`](internal/builder)), the generated
ClusterRole ([`config/rbac/role.yaml`](config/rbac/role.yaml)), the chart's
hand-written one
([`deploy/helm/mosquitto-operator/templates/clusterrole.yaml`](deploy/helm/mosquitto-operator/templates/clusterrole.yaml)),
the rendered CRD schema
([`config/crd/bases/mko.gtrfc.com_mosquittoes.yaml`](config/crd/bases/mko.gtrfc.com_mosquittoes.yaml)),
the entrypoint ([`cmd/main.go`](cmd/main.go)), the image build
([`Containerfile`](Containerfile), [`.dockerignore`](.dockerignore)) and all
three workflows ([`.github/workflows/`](.github/workflows)). The RBAC tables in
section 4 come from running `make verify-rbac-parity` and rendering both install
paths, not from reading YAML by eye.

**Nothing described here has ever been observed on a real cluster from this
repository.** There is an E2E suite and there are integration tests against
envtest, but no run of either backs any claim in this document; where the
distinction matters — kubelet mount semantics, what the metrics endpoint really
emits — it is marked inline. The same caveat is recorded in the ADRs.

Related reading: [README.md](README.md) for the user-facing posture,
[DEVELOPER.md](DEVELOPER.md) for the code layout and the reconcile flow, and the
ADRs that own each decision —
[ADR 0001](docs/adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md)
(TLS material is consumed, never issued),
[ADR 0005](docs/adr/0005-fork-pull-requests-execute-on-the-self-hosted-runners.md)
(the accepted fork-PR risk),
[ADR 0006](docs/adr/0006-both-install-paths-grant-the-same-authority.md)
(why both install paths grant the same thing),
[ADR 0008](docs/adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
(the anonymous posture and `spec.config`) and
[ADR 0009](docs/adr/0009-delete-only-through-owner-references.md)
(delete only through owner references, and never patch).

---

## 1. Roles and trust boundaries

| Principal | Identity | Scope | Trusted with |
|---|---|---|---|
| **Cluster administrator** | Whoever installs the chart or applies `kustomize build config/default` | Cluster | Creates the ClusterRole and the ClusterRoleBinding, and the CRD — from the chart, or separately from `config/crd` via `make install`, because `kustomize build config/default` renders no CustomResourceDefinition; decides whether the operator's metrics port is open (`metrics.enabled`) and whether any NetworkPolicy exists at all — none ships here |
| **Operator manager** | ServiceAccount named by the chart's `fullname` helper — `<release>-mosquitto-operator`, or just `mosquitto-operator` when the release is itself called `mosquitto-operator`, which is what the README's install command does — or `mosquitto-operator-mosquitto-operator` (kustomize, in `mosquitto-operator-system`), bound by a **ClusterRoleBinding** | **Cluster-wide, every namespace** | `create`/`get`/`list`/`update`/`watch` on `configmaps`, `services` and `statefulsets`; `get`/`list`/`watch` on `mosquittoes`; `update` on `mosquittoes/status` and `mosquittoes/finalizers`. No `delete` anywhere, no `patch` anywhere, and **no rule on `secrets`** (section 4) |
| **CR author** | Anyone with `create`/`update` on `mosquittoes.mko.gtrfc.com` in a namespace | That namespace | Chooses `spec.image` (arbitrary string), the whole tail of the broker's `mosquitto.conf` through `spec.config`, the TLS Secret name, `spec.replicas` (1–9), the storage class. Section 3 is what that buys them |
| **Broker pods** | The namespace's `default` ServiceAccount — `buildPodSpec` sets no `ServiceAccountName` — with `AutomountServiceAccountToken: ptr.To(false)` ([`internal/builder/statefulset.go:154`](internal/builder/statefulset.go)) | None: no token is mounted | Nothing. The container command is `/usr/sbin/mosquitto -c /mosquitto/config/mosquitto.conf`; no code in this repository runs in that pod and it makes no Kubernetes API call |
| **MQTT clients** | **None. There is no client identity.** | Anything that can route to the ClusterIP Service `<name>` on 1883, or 8883 under TLS | Publish and subscribe on every topic. `GenerateMosquittoConf` appends `allow_anonymous true` unconditionally and renders no `require_certificate` ([`internal/builder/configmap.go:129`](internal/builder/configmap.go)) |
| **CI** | GitHub Actions jobs, all on `self-hosted` runners | The runner fleet; on non-fork runs also Docker Hub and this GitHub repository | `DOCKERHUB_PAT`, `BOT_PAT` and the job `GITHUB_TOKEN` — section 2.4 says which job holds which, and what bounds it |

```
        cluster scope                              namespace scope (any namespace)
  ┌──────────────────────────────┐        ┌────────────────────────────────────────┐
  │ ClusterRole                  │        │ Mosquitto <name>       (the CR author   │
  │   configmaps, services       │        │                         writes this)   │
  │   statefulsets               │        │   ├── ConfigMap   <name>-config         │
  │     create/get/list/update/  │        │   │     mosquitto.conf, spec.config     │
  │     watch                    │        │   │     appended verbatim               │
  │   mosquittoes  get/list/watch│        │   ├── Service     <name>-headless       │
  │   .../status      update     │        │   ├── Service     <name>       :1883    │
  │   .../finalizers  update     │        │   └── StatefulSet <name> ── broker pods │
  └───────────────┬──────────────┘        └────────────────────────────────────────┘
                  │ ClusterRoleBinding                     ▲
                  ▼                                        │ creates + owns
  ┌──────────────────────────────┐                         │ (ownerReference)
  │ SA <release>-mosquitto-      │─────────────────────────┘ never deletes,
  │    operator                  │                           never patches,
  │ operator Deployment          │                           no `secrets` rule
  │ (release namespace)          │
  │ :8080 metrics  :8081 health  │  both plain HTTP, no auth filter
  └──────────────────────────────┘

  TLS Secret (user-owned)  ──kubelet mounts it read-only──►  broker pod
  spec.tls.secretName            at /mosquitto/tls              the operator
                                                                never reads it

  MQTT client ──TCP 1883 (8883 under TLS)──► Service <name> ──► broker pods
                                    no authentication of any kind
```

**The two boundaries that matter.**

1. **Operator ↔ workload.** The operator is cluster-wide and can create or
   overwrite a ConfigMap, a Service or a StatefulSet in **every** namespace.
   Replacing a StatefulSet's pod template is replacing the code that runs, so a
   compromised operator is a cluster-wide problem — bounded only by the verbs it
   does not hold (section 4).
2. **CR author ↔ broker.** There is no boundary between them worth the name.
   `spec.config` is appended to the generated file verbatim and the broker reads
   it last, so whoever may write a `Mosquitto` in a namespace writes that
   broker's entire configuration: listeners, bridges, log destinations
   ([ADR 0008](docs/adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
   D6, D7, D10). RBAC on `mosquittoes` is the only control surface for it, and
   there is no second one.

What is deliberately **not** on this list, because it does not exist in this
repository: admission webhooks, NetworkPolicies, PodDisruptionBudgets,
ServiceMonitors, PrometheusRules, and any per-instance ServiceAccount, Role or
RoleBinding. A broker metrics exporter sidecar is a **recorded decision with no
code behind it** ([ADR 0002](docs/adr/0002-the-metrics-exporter-is-written-here.md)):
`spec.metrics` is not in the API, `cmd/exporter` does not exist, and
`buildPodSpec` builds exactly one container. It is named here so that nothing
in this document is read as describing it.

---

## 2. Data and secret flow

### 2.1 The operator holds no workload credential

There is no `secrets` rule in either install path, and the chart says so in a
comment rather than by omission. `SetupWithManager` registers
`For(&mkov1.Mosquitto{})` plus `Owns` on `appsv1.StatefulSet`, `corev1.ConfigMap`
and `corev1.Service` — no `Watches`, and nothing on `Secret`
([`internal/controller/mosquitto_controller.go:348-351`](internal/controller/mosquitto_controller.go)).
**Not watching the TLS Secret is not an oversight; it is the privilege boundary**
([ADR 0001](docs/adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md)
D6), and section 6 states what it costs.

### 2.2 TLS material

| Step | What happens | Read from |
|---|---|---|
| The Secret is created | By hand (`kubectl create secret tls`) or by a cert-manager `Certificate` the administrator owns. **Never by this operator** — no cert-manager dependency in [`Chart.yaml`](deploy/helm/mosquitto-operator/Chart.yaml), no cert-manager module in [`go.mod`](go.mod) | [ADR 0001](docs/adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md) D1–D3 |
| The pod references it | `buildPodSpec` adds a `corev1.SecretVolumeSource` with `SecretName: m.Spec.TLS.SecretName`, `DefaultMode` `0o644` and **no `Items` projection** | [`internal/builder/statefulset.go`](internal/builder/statefulset.go) |
| The kubelet mounts it | At `TLSMountPath = "/mosquitto/tls"`, `ReadOnly: true`. Because there is no projection, **every key the Secret carries appears in that directory**, not only the two the config names | [`internal/builder/configmap.go`](internal/builder/configmap.go), [`internal/builder/statefulset.go`](internal/builder/statefulset.go) |
| The broker reads it | The generated block names `certfile /mosquitto/tls/tls.crt` and `keyfile /mosquitto/tls/tls.key` — `TLSCertKey` and `TLSKeyKey` — once, at process start | [`internal/builder/configmap.go`](internal/builder/configmap.go) |

The private key therefore travels kubelet → volume → broker process and never
passes through the operator. A compromised operator leaks no TLS material it was
not already leaking by scheduling the pods that mount it.

### 2.3 Where credentials can end up by accident

- **`spec.config` is copied into a ConfigMap.** `BuildConfigMap` writes the
  rendered file into `<name>-config` under the key `mosquitto.conf`. A credential
  placed in `spec.config` is then readable twice: with `get mosquittoes` and with
  `get configmaps` in that namespace — and a ConfigMap is not covered by a
  Secret-only KMS configuration. This is why
  [ADR 0008](docs/adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
  D11 fixes now that any future credential surface is modelled as a Secret
  reference, never as `spec.config` content.
- **Nothing else.** The reconciler writes no credential into `.status`, and it
  records **no Events at all** — verified: no `EventRecorder`, `Recorder` or
  `Eventf` anywhere in `internal/`, `cmd/` or `api/`. The `events` rule that does
  exist belongs to client-go's leader-election `LeaseLock`, not to the
  reconciler ([`config/rbac/leader_election_role.yaml`](config/rbac/leader_election_role.yaml)).
- **Broker logs go to stdout.** The generated file sets `log_dest stdout` with
  `log_type` `error`, `warning`, `notice` and `information`, so broker output is
  whatever `kubectl logs` shows and inherits the cluster's log-retention posture.

### 2.4 CI credentials

Three credentials appear in the workflows. None of them reaches a fork pull
request: GitHub does not pass repository secrets to a `pull_request` run from a
fork, so in a fork run `secrets.DOCKERHUB_PAT` and `secrets.BOT_PAT` are empty
strings and the job token is read-only
([ADR 0005](docs/adr/0005-fork-pull-requests-execute-on-the-self-hosted-runners.md)
D4).

| Credential | Which job holds it | What for | What bounds it |
|---|---|---|---|
| `DOCKERHUB_PAT` | [`release.yml`](.github/workflows/release.yml): `e2e-tests`, `mosquitto-image-tools`, `container-malware-scan`, each in a `docker/login-action@v4` step. [`build.yml`](.github/workflows/build.yml): the `build` job's login and its `docker/scout-action@v1` step | Authenticated Docker Hub pulls (the anonymous rate limit is the reason the image-tools job logs in at all) and, in `build.yml`, the release push and the Scout scan | `container-malware-scan` runs `docker logout` **before** its two Trivy steps, so third-party action code executes with no credential left in `~/.docker/config.json`; that action is pinned to a commit (`aquasecurity/trivy-action@ed142fd…`), unlike the tag-referenced first-party actions |
| `BOT_PAT` | [`release.yml`](.github/workflows/release.yml): `semantic-release` (the checkout `token:` and the `GITHUB_TOKEN` env of the Release step). [`renovate.yml`](.github/workflows/renovate.yml): the `renovate` job | Tagging, publishing the release, committing the coverage badge; Renovate's own PRs | `semantic-release` carries `if: github.event_name == 'push' && github.ref == 'refs/heads/main'`, so it never runs on a pull request of any kind; its checkout sets `persist-credentials: false` so the token is not written into `.git/config` as an `http.extraheader`; and `npm ci --ignore-scripts` keeps dependency lifecycle scripts from executing while the token sits in the environment. `renovate.yml` has no `pull_request` trigger at all |
| `GITHUB_TOKEN` (the job token) | [`release.yml`](.github/workflows/release.yml): `coverage-report`'s checkout. [`build.yml`](.github/workflows/build.yml): the SBOM upload, the release-asset upload and the `gh-pages` checkout | Sticky PR comment, release assets, publishing the Helm chart by committing to `gh-pages` | `release.yml` and `renovate.yml` set a top-level `permissions: contents: read` floor; exactly two jobs raise it, each in its own block (`coverage-report`: `pull-requests: write`; `semantic-release`: `contents`, `issues`, `pull-requests`, `id-token` write). [`build.yml`](.github/workflows/build.yml) has **no top-level floor** — both of its jobs declare `contents: write` themselves, so a job added without a block would inherit the repository default (open item, section 7) |

Two mechanisms keep a job token out of the container image, and the comments in
both files say plainly that either one alone would be a single point of failure:
the `Containerfile` does `COPY . .`, so [`.dockerignore`](.dockerignore) excludes
`.git/`, **and** the two jobs that build an image (`container-malware-scan`,
`build`) check out with `persist-credentials: false`.

---

## 3. Isolation and tenancy

### What holds

- **Every managed object carries a controller ownerReference**, set by
  `controllerutil.SetControllerReference` before the write, on the ConfigMap, both
  Services and the StatefulSet. Deleting a `Mosquitto` therefore removes the whole
  workload through garbage collection, and the operator needs no `delete` verb to
  achieve it ([ADR 0009](docs/adr/0009-delete-only-through-owner-references.md) D1, D2).
- **A pre-existing object holding a derived name is refused, never adopted.**
  `ensureOwned` uses `metav1.IsControlledBy`, which matches on the controller
  reference **and its UID**, and the refusal travels up as a reconcile failure:
  `PhaseFailed`, `Ready=False`, reason `ReconcileFailed`, message = the error
  text. The Services are the sharpest case — `spec.selector` of a live Service is
  mutable, so nothing at the API level would stop the operator from repointing
  somebody else's Service at these pods. The ownership check is that stop
  ([ADR 0009](docs/adr/0009-delete-only-through-owner-references.md) D5).
- **The broker pods are hardened, and admit into a `restricted` PSA namespace.**
  From `buildPodSpec` and `buildBrokerContainer`
  ([`internal/builder/statefulset.go:154-166,224-228`](internal/builder/statefulset.go)):
  `AutomountServiceAccountToken: false`, `RunAsNonRoot: true`,
  `RunAsUser`/`RunAsGroup`/`FSGroup` all `brokerUserID = 1883`,
  `SeccompProfile: RuntimeDefault` at pod level, and on the container
  `AllowPrivilegeEscalation: false`, `ReadOnlyRootFilesystem: true`,
  `Capabilities.Drop: [ALL]`. The image entrypoint is bypassed
  (`Command: /usr/sbin/mosquitto -c …`) precisely because it chowns `/mosquitto`
  when it runs as root, which this pod never does.
- **Anti-affinity is per resource.** `BuildPodAntiAffinity` selects on
  `common.SelectorLabels(m)`, which contains `app.kubernetes.io/instance: <name>`,
  so one `Mosquitto` never repels another's pods.

### What does not hold

Read this before treating a namespace as a tenant boundary.

- **Every broker is anonymous, always.** Not "by default in some configurations":
  `GenerateMosquittoConf` appends `allow_anonymous true` on both the plaintext
  and the TLS branch, unconditionally, on every `Mosquitto` the current API can
  express. Anything that can open a TCP connection to the broker can publish and
  subscribe to every topic. The API models no authentication field of any kind
  ([ADR 0008](docs/adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
  D1). The generated file states this in its own comments, so
  `kubectl get configmap <name>-config -o yaml` is a complete answer.
- **TLS changes the transport, not the trust.** With `spec.tls` set the generated
  block emits `listener 8883`, `certfile` and `keyfile`, and nothing else — the
  string `require_certificate` occurs nowhere in the generator. TLS encrypts the
  connection and proves the broker's identity to clients; it authenticates **no
  client to the broker**. An encrypted anonymous broker is still an anonymous
  broker ([ADR 0001](docs/adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md)
  D9).
- **`spec.config` is appended verbatim and wins.** The only transformations are
  `strings.TrimSpace` for the emptiness check and `strings.TrimRight(…, "\n")` on
  the content. A repeated global option overrides the generated one — including
  `allow_anonymous false`, which is a supported and tested use of the field — and
  an added `listener` line creates a listener the operator neither models nor
  exposes as a container or Service port. A `spec.config` bridge block forwards
  messages to an external broker, and nothing in this repository would show that
  anywhere except in the CR the author wrote.
- **"Enabling TLS closes the plaintext port" is a guarantee about the generated
  block, not about the file.** The doc comment on `BrokerPort` says so in as many
  words. A `spec.config` carrying `listener 1883` reopens plaintext, and the
  missing container port declares nothing about reachability: `containerPort` is
  documentation, not a firewall, so any pod that can route to the broker pod's IP
  reaches any port the process listens on
  ([ADR 0008](docs/adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
  D8). Nothing detects this case.
- **No NetworkPolicy is shipped**, for the brokers or for the operator. The
  network boundary that the entire anonymous posture leans on is the cluster
  administrator's to build. The chart says this on purpose, in
  [`values.yaml`](deploy/helm/mosquitto-operator/values.yaml) and in
  [`templates/service.yaml`](deploy/helm/mosquitto-operator/templates/service.yaml):
  a policy fitting one cluster's CNI and monitoring topology fits few others.
- **`spec.image` is an arbitrary string.** No registry allowlist, no digest
  requirement, no pattern in the schema. The CR author picks the image the broker
  pods run.
- **Namespace is not a boundary for the operator itself.** The binding is a
  ClusterRoleBinding; the manager reconciles `Mosquitto` resources in every
  namespace and holds `create`/`update` on `configmaps`, `services` and
  `statefulsets` everywhere. `ensureOwned` bounds the *reconcile path*, not the
  *grant*: a compromised operator is not subject to its own guard.
- **PVCs outlive the resource.** `buildVolumeClaimTemplates` produces a template
  named `data`; the StatefulSet controller creates the claims, and this operator
  holds no `delete` verb on anything and sets no
  `persistentVolumeClaimRetentionPolicy`. Deleting a `Mosquitto` therefore leaves
  the broker's persistence volumes on disk. Without `spec.storage` the data
  directory is an `emptyDir` and dies with the pod instead.
- **Uninstalling the chart deletes the CRD.** The CRD is a normal template
  ([`templates/crd.yaml`](deploy/helm/mosquitto-operator/templates/crd.yaml)) with
  no `helm.sh/resource-policy: keep` annotation, so `helm uninstall` removes it —
  and removing a CRD removes every `Mosquitto` object and, through the owner
  references, every broker workload in the cluster. `make uninstall` carries the
  same warning in its comment. The PVCs survive; nothing else does.

---

## 4. Privilege footprint

The operator ships two install paths and they are meant to grant the same thing:
`helm install` from
[`deploy/helm/mosquitto-operator`](deploy/helm/mosquitto-operator), and
`kustomize build config/default | kubectl apply -f -`.

The kustomize path is two commands, not one: `config/default` composes only
`../rbac` and `../manager` and renders six objects — ServiceAccount, Role,
ClusterRole, RoleBinding, ClusterRoleBinding, Deployment — and **no
CustomResourceDefinition**. The CRD lives in `config/crd` and is applied by
`make install`, which runs `config/crd` and `config/rbac` in that order. Applying
`config/default` alone leaves an operator running against a CRD that may not
exist. The chart has no such split: `templates/crd.yaml` ships with it.

Only the kustomize path is generated — `make generate-all` runs controller-gen over the markers and writes
[`config/rbac/role.yaml`](config/rbac/role.yaml); the chart's ClusterRole is
written by hand and nothing regenerates it. `make verify-rbac-parity` renders
both and compares them as `(kind, apiGroup, resource) → verb set`
([`test/rbacparity/rbac_parity_test.go`](test/rbacparity/rbac_parity_test.go)),
and the `generated-manifests` job runs it on every push to `main` and every
pull request.

`make verify-rbac-parity`, run for this document: **PASS, "compared 8 grants
across both install paths".** Those eight, collapsed into seven rows because `configmaps` and `services` carry an
identical rule and controller-gen emits them as one. The consequence of each:

| Kind | API group | Resources | Verbs | What it permits |
|---|---|---|---|---|
| ClusterRole | `mko.gtrfc.com` | `mosquittoes` | get, list, watch | Read every `Mosquitto` in the cluster. **No `create`, `update` or `delete`** — the reconciler never writes a CR spec |
| ClusterRole | `mko.gtrfc.com` | `mosquittoes/status` | update | Status authority. `update` alone because `Status().Update()` is a PUT and nothing reads status on its own |
| ClusterRole | `mko.gtrfc.com` | `mosquittoes/finalizers` | update | Required by the `OwnerReferencesPermissionEnforcement` admission plugin — off by default, on in some managed distributions — because the owner references the reconciler writes carry `blockOwnerDeletion`. Without it the operator would create nothing on those clusters |
| ClusterRole | `""` (core) | `configmaps`, `services` | create, get, list, update, watch | **Can create or overwrite any ConfigMap or Service in any namespace.** Overwriting a Service's `spec.selector` redirects that traffic; overwriting a ConfigMap changes what its consumers read |
| ClusterRole | `apps` | `statefulsets` | create, get, list, update, watch | **Can replace the pod template — hence the image, hence the code — of any StatefulSet in the cluster.** The heaviest grant here |
| Role (release namespace) | `coordination.k8s.io` | `leases` | create, delete, get, list, patch, update, watch | Leader election. Namespaced on purpose: the Lease `mosquitto-operator.mko.gtrfc.com` (`LeaderElectionID` in [`cmd/main.go`](cmd/main.go)) lives in the operator's own namespace, so a ClusterRole over every Lease would be reach the manager never uses |
| Role (release namespace) | `""` (core) | `events` | create, patch | client-go's `LeaseLock` records a `LeaderElection` Event when leadership changes. The reconciler records no Events |

**What is absent is the interesting half**, and it is absent by decision rather
than by oversight ([ADR 0009](docs/adr/0009-delete-only-through-owner-references.md)
D2–D4):

- **No `delete` and no `patch` in the ClusterRole.** Teardown is the garbage
  collector's job through the owner references, and every write is `Create` or
  `Update`; a grep for `.Patch(` and `.Delete(` over the non-test tree returns
  nothing. A capability that does not exist cannot be aimed at the wrong object
  by a bug. If server-side apply is ever adopted, `patch` comes back with the
  code that needs it.
- **The exception, stated so the sentence above stays true:** the namespaced
  leader-election `Role` grants `create,delete,get,list,patch,update,watch` on
  `coordination.k8s.io/leases` and `create,patch` on `events`. That is client-go's
  `LeaseLock` operating on the operator's own Lease in its own namespace, not the
  reconciler reaching a managed object. Both install paths render it identically
  (section 4).
- **No rule on `secrets`.** Not narrowed — absent. See section 2.1.
- **No `escalate`, no `bind`, nothing on `rbac.authorization.k8s.io`, no
  `serviceaccounts`.** The operator creates no per-instance identity, so it needs
  no authority to grant one.
- `list` and `watch` are informer verbs, not call sites: controller-runtime's
  cache needs them for every kind the manager watches, even though no line of the
  non-test tree calls `List`.

**Honest summary.** This is a workload manager with cluster-wide create/update on
three kinds and read on its own CRD. It is not a cluster-admin equivalent — it
cannot read a Secret, cannot delete any object it manages, cannot write RBAC. It is also not
harmless: `statefulsets: create,update` in every namespace is the authority to
run chosen code as any workload identity in the cluster. Treat its ServiceAccount
token and its image accordingly.

### 4.1 Two differences the parity test does not cover

The test compares RBAC, at **chart default values**, and that scope is worth
stating because both halves are load-bearing:

- **Rendered with `leaderElection.enabled=false` the chart emits no `Role` at
  all**, while `config/default` always includes
  [`config/rbac/leader_election_role.yaml`](config/rbac/leader_election_role.yaml)
  and [`config/manager/manager.yaml`](config/manager/manager.yaml) always passes
  `--leader-elect`. Verified by rendering the chart with the flag off: zero
  `kind: Role` documents. The parity statement is therefore "the two paths agree
  at default values", not "the chart cannot be configured into a different
  shape".
- **The metrics port is configurable in one path only.** The chart renders
  `--metrics-bind-address=:8080` or `=0` from `metrics.enabled` (default `true`),
  where `0` is the literal controller-runtime reads as "do not start the metrics
  server" — verified by rendering with `metrics.enabled=false`. `manager.yaml`
  passes no `--metrics-bind-address` at all, so the kustomize path always takes
  the default `:8080` from [`cmd/main.go`](cmd/main.go) and can only be changed
  by editing the manifest.

### 4.2 Operator process posture

Identical hardening in both paths, and the comment in
[`config/manager/manager.yaml`](config/manager/manager.yaml) says that is
deliberate — two shipped install paths for the same image must not produce
materially different pods. Pod level: `runAsNonRoot: true`,
`seccompProfile: RuntimeDefault`, `terminationGracePeriodSeconds: 10`. Container
level: `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true`,
`capabilities: drop [ALL]`. The runtime base image is
`gcr.io/distroless/static-debian12:nonroot` and the binary is built
`CGO_ENABLED=0` ([`Containerfile`](Containerfile)).

Two ports, both plain HTTP with no authentication or authorization filter:
`:8080` metrics and `:8081` health (`/healthz`, `/readyz`, both
`healthz.Ping`). **Anything that can route to the operator pod reads `:8080`,
with or without the optional `-metrics` Service** — that Service adds a DNS name
and a selector, not reach. Only `metrics.enabled=false` closes the port.

What `:8080` discloses: controller-runtime's own registry. **This repository
registers no collector of its own** — verified: no `prometheus` import anywhere
in `internal/`, `cmd/` or `api/`, and `github.com/prometheus/client_golang`
appears in [`go.mod`](go.mod) only as an indirect dependency. Exactly which
series that registry exposes on this version was **not measured here**; what can
be said from the code is that no Mosquitto name, namespace, image or spec content
is published by anything this repository wrote.

---

## 5. Validation story

**There is no admission webhook in this project** — no
`ValidatingWebhookConfiguration`, no `MutatingWebhookConfiguration`, no
`config/webhook` directory, nothing in the chart. Everything that validates a
`Mosquitto` is CRD schema validation generated from the kubebuilder markers in
[`api/v1/mosquitto_types.go`](api/v1/mosquitto_types.go).

| Field | What the schema enforces |
|---|---|
| `spec.replicas` | `type: integer`, `format: int32`, `minimum: 1`, `maximum: 9`, `default: 1` |
| `spec.antiAffinity` | `enum: ["off", soft, hard]`, `default: "off"` |
| `spec.tls.secretName` | `minLength: 1`, and `required` within the `tls` object |
| `spec.storage.size` | `minLength: 1`, and `required` within the `storage` object |
| `spec.image` | `type: string`. Nothing else |
| `spec.config` | `type: string`. No `maxLength`, no `pattern` |
| `spec.resources` | The standard `corev1.ResourceRequirements` schema |

`TestIntegration_CRD_RejectsInvalidSpecs`
([`test/integration/crd_validation_test.go`](test/integration/crd_validation_test.go))
pins four of those against a real API server: ten replicas, an unknown
anti-affinity mode, an empty TLS secret name and an empty storage size are all
rejected on `Create`.

**What nothing validates:**

- **`spec.config`.** Deliberate, not an omission
  ([ADR 0008](docs/adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
  D9): validating it means reimplementing Mosquitto's configuration parser for an
  image this repository consumes and does not build, and keeping it in step as
  the option set moves between versions. The broker sees the file first at
  startup, so a rejected configuration is a `CrashLoopBackOff`, not a rejected
  `kubectl apply`.
- **`spec.image`.** Any registry, any tag, any digest, no allowlist.
- **The referenced TLS Secret.** Its existence, its keys, and whether the key
  matches the certificate are all unchecked, because the operator cannot read it
  (section 2.1). A `secretName` pointing at nothing produces a StatefulSet that
  reconciles cleanly and pods that never start: the reconcile is green, the
  broker is in `CrashLoopBackOff`.
  [`test/integration/tls_test.go`](test/integration/tls_test.go) creates a Secret
  whose values are the literal strings `not-a-certificate` and `not-a-key` and
  the operator is perfectly happy — envtest runs no kubelet, so nothing ever
  tries to parse them.
- **Cross-field rules.** There are no CEL rules: a grep for
  `x-kubernetes-validations` over the rendered CRD returns nothing. The only
  `x-kubernetes-*` keys present come from the embedded `ResourceRequirements`
  schema.
- **`spec.storage.size` as a quantity.** `minLength: 1` is all the schema knows.
  An unparsable value is caught later by `resource.ParseQuantity` in
  `buildVolumeClaimTemplates`, which fails the reconcile — a visible
  `PhaseFailed`, deliberately, because that value reaches an immutable PVC
  template.
- **The resource name.** Kubebuilder markers cannot constrain `metadata.name`, and
  every managed name is derived from it: `<name>`, `<name>-headless`,
  `<name>-config`. Whether a long name produces a derived name the API server
  rejects, and what that looks like, was **not tested here**.

---

## 6. Rotation and change propagation

The StatefulSet pod template carries two annotations, and
`StatefulSetHasChanged` compares exactly those two plus the replica count, the
object labels and the template labels
([`internal/builder/statefulset.go`](internal/builder/statefulset.go)):

- `mko.gtrfc.com/pod-spec-hash` (`AnnotationPodSpecHash`) — `hashOf(podSpec)`
- `mko.gtrfc.com/config-hash` (`AnnotationConfigHash`) — `hashOf(GenerateMosquittoConf(m))`

`hashOf` is a 32-bit FNV-1a digest of the JSON encoding, and its own comment
scopes it: *"only used to detect change, never to prove identity"*. It is not an
integrity control and nothing in this operator treats it as evidence about
anything but its own desired object — anyone who could rewrite the annotation
already holds `update` on the StatefulSet and could rewrite the pod template
outright.

| Change | Reaches running pods? | Mechanism |
|---|---|---|
| `spec.image`, `spec.resources`, `spec.antiAffinity`, `spec.replicas` | Yes | They are in the pod spec (or the replica count), so the hash changes and the StatefulSet controller rolls the pods |
| `spec.config`, and anything else in the generated file | Yes | `AnnotationConfigHash` digests the rendered `mosquitto.conf`. Mosquitto reads its configuration once at startup and a ConfigMap update restarts nothing, so without this annotation a config change would sit in the ConfigMap and never reach a running broker. Turning `allow_anonymous` off therefore costs a broker restart — the desired behaviour |
| `spec.tls.secretName` pointing at a **different** Secret | Yes | The name is part of the pod spec, so the pod-spec hash changes. Derived by reading `buildPodSpec` and `hashOf`; **not pinned by a test** that renames a Secret |
| **New bytes inside the referenced Secret** (a renewal) | **No** | See below |
| `spec.storage` after creation | **No** | `volumeClaimTemplates` are immutable, and `reconcileStatefulSet` deliberately writes only `Spec.Replicas`, `Spec.Template` and the labels. A changed `spec.storage` needs the StatefulSet recreated by hand |

### The TLS rotation gap, stated precisely

The operator does not watch the Secret and holds no permission to read one, so
neither digest has ever seen its bytes. When cert-manager renews, or when
somebody replaces the material by hand:

1. The Secret changes. The file under `/mosquitto/tls` is expected to follow it,
   because the mount uses no `subPath` — that half is standard Kubernetes
   Secret-volume behaviour and **was not verified against a cluster here**, only
   the absence of `subPath` was.
2. The running broker keeps presenting the certificate it parsed at start. There
   is no event, no condition and no log line anywhere in this operator that marks
   the moment.
3. Nothing converges until the pods restart — for example
   `kubectl rollout restart statefulset/<name>`.

**The consequence is an outage on a timer, on exactly the clusters that
automated issuance was supposed to protect:** a long-lived pod outlives its own
certificate. The same sentence is written on
`MosquittoTLS.SecretName`, in the chart's
[`values.yaml`](deploy/helm/mosquitto-operator/values.yaml) and in
[README.md](README.md), so a user meets it wherever they arrive.

What would close it is named and deliberately not built
([ADR 0001](docs/adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md)
D8): a third annotation digesting the *content* of the referenced Secret, so a
rotation changes the pod template on its own. The price is exactly the privilege
D6 spends — `get`, `list` and `watch` on `secrets`, plus a `Watches` mapping
Secrets back to the referencing `Mosquitto`, since `Owns` does not cover an object
the operator did not create. The digest itself is not the objection (a
certificate is public and a private key is not recoverable from a digest); the
read permission is.

---

## 7. Residual risks — hardening checklist

Ordered by what the exposure buys, not by effort. Each item names the principal,
the verb, the target, and whether it is live today.

- [ ] **Any pod that can route to `<name>:1883` can publish and subscribe to
      every topic. Live, on every `Mosquitto` this operator can currently
      express.** `allow_anonymous true` is unconditional and the API models no
      authentication. Today's only mitigations are outside this repository: a
      NetworkPolicy the cluster administrator writes, and RBAC on who may create
      a `Mosquitto` at all. The real answer is
      [ADR 0008](docs/adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
      D11/D12 — the `password-file` and `acl-file` plugins, modelled as API
      fields. **Not implemented.**
- [ ] **A namespace user with `create mosquittoes` writes that broker's entire
      configuration file, unvalidated. Live.** Extra listeners on ports no
      Kubernetes object declares, a bridge forwarding messages to an external
      broker, redirected logs. Nothing in this repository reports any of it.
      Action: treat `create`/`update` on `mosquittoes.mko.gtrfc.com` as the
      privilege it is, and review `spec.config` in whatever reviews reach your
      cluster (GitOps, admission policy of your own).
- [ ] **A renewed certificate never reaches a running broker. Live, and dormant
      until the first renewal — at which point it is an availability failure, not
      an attack.** No principal is needed. Action: restart the pods after every
      renewal (`kubectl rollout restart statefulset/<name>`) and watch expiry
      from outside this operator, because nothing in it will tell you. Section 6
      and [ADR 0001](docs/adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md)
      D7/D8 carry the mechanism and the price of closing it.
- [ ] **Anything that can route to the operator pod reads `:8080/metrics`
      unauthenticated, in plain HTTP. Live by default** (`metrics.enabled: true`,
      and the kustomize path has no switch at all). What it discloses is
      controller-runtime's own registry — this repository registers no collector,
      so no resource name, spec field or Secret is published by anything written
      here; the exact series were not measured (section 4.2). Actions:
      `metrics.enabled=false`, which passes `--metrics-bind-address=0`, or a
      NetworkPolicy restricting ingress to the operator namespace. Deleting the
      `-metrics` Service only removes a DNS name.
- [ ] **Whoever holds the operator's ServiceAccount token, or can change its
      image, can create or overwrite a ConfigMap, a Service or a StatefulSet in
      every namespace. Live.** Replacing a pod template is replacing the code
      that runs. It cannot delete, cannot patch, and cannot read a Secret — that
      is the bound. Narrowing the ClusterRole to the namespaces actually served
      would cost the install-and-forget property for new namespaces; it has not
      been done and is a deliberate open trade.
- [ ] **No NetworkPolicy ships for broker pods or for the operator. Live.** The
      network is the only boundary the anonymous posture has, and this project
      builds none of it. The chart states the reason
      ([`values.yaml`](deploy/helm/mosquitto-operator/values.yaml)): a policy that
      fits one CNI and monitoring topology fits few others.
- [ ] **A fork pull request executes fork-authored code on the self-hosted runner
      fleet. Live, and accepted** by the maintainer on 2026-09-01
      ([ADR 0005](docs/adr/0005-fork-pull-requests-execute-on-the-self-hosted-runners.md)).
      Stated with the correct facts, because the wrong version of this sentence
      is the common one: **secrets are not the exposure.** GitHub passes no
      repository secret to a `pull_request` run from a fork — `DOCKERHUB_PAT` and
      `BOT_PAT` are empty strings there and the job token is read-only — so what
      is at stake is **code execution on the runner**, on a fleet whose steps
      assume a Docker daemon and unprompted `sudo`. Under a `pull_request`
      trigger the workflow definition itself comes from the PR, so no part of the
      repository is out of a fork author's reach. The control is a repository
      setting, not a file: *Settings > Actions > General > "Fork pull request
      workflows from outside collaborators" = "Require approval for all outside
      collaborators"*. **This repository cannot see that setting and this document
      does not claim it is set.** For a maintainer the operational consequence is
      concrete: approval is per run, a push to an approved PR needs approving
      again, and approving means having read the diff of
      [`.github/workflows/`](.github/workflows), the
      [`Containerfile`](Containerfile) and the [`Makefile`](Makefile).
- [ ] **Runner isolation is asserted, not verified. Open, and it is the path from
      "code execution" to "secrets".** The matrix comment describes "its own
      ephemeral ARC runner pod". If the runners are not ephemeral, fork-authored
      code can leave state — a poisoned build cache, a modified
      `~/.docker/config.json`, a cron entry — that a later trusted job executes
      with `DOCKERHUB_PAT` or `BOT_PAT` in its environment. That is infrastructure
      this repository cannot inspect.
- [ ] **Renovate automerges GitHub Actions minor, patch and digest updates, and
      those actions execute on the fleet in jobs that hold `DOCKERHUB_PAT` and
      `BOT_PAT`. Live.** Majors require review ([`renovate.json`](renovate.json)),
      and only `aquasecurity/trivy-action` is pinned to a commit — the rest keep
      tag refs, on the argument that they are first-party or vendor-published.
      Dropping `digest` from the github-actions automerge rule is the one-line
      change if human review is what is wanted.
- [ ] **[`build.yml`](.github/workflows/build.yml) has no top-level `permissions:`
      floor. Dormant.** Both current jobs declare `contents: write` themselves, so
      the file is correct as it stands; a job added without a block inherits the
      repository-wide default, which may be read/write on all scopes.
      [`release.yml`](.github/workflows/release.yml) and
      [`renovate.yml`](.github/workflows/renovate.yml) do not have this problem.
      One line fixes it.
- [ ] **`helm uninstall` deletes the CRD and with it every broker in the cluster.
      Live, and it needs no attacker — a cluster administrator running the obvious
      command is enough.** The CRD is a plain template with no
      `helm.sh/resource-policy: keep`. The PVCs survive; the `Mosquitto` objects,
      StatefulSets, Services and ConfigMaps do not.
- [ ] **Deleting a `Mosquitto` leaves its PVCs behind. Live, by design.** The
      operator holds no `delete` verb and sets no
      `persistentVolumeClaimRetentionPolicy`, so broker persistence files stay on
      disk until someone removes the claims by hand. Deliberate — the data is not
      the operator's to destroy — but it is retained data nobody is tracking.
- [ ] **Nothing here has run against a real cluster from this repository. Open,
      and it bounds every claim above.** The integration tier runs against
      envtest, which has no kubelet, so no pod in these tests ever started, no
      Secret was ever parsed and no MQTT connection was ever made. The E2E suite
      exists and was not run for this document.

---

## 8. How to report a vulnerability

This repository has **no `SECURITY.md`** and no published contact address —
stated rather than invented. (`SECURITY.md` is GitHub's vulnerability-reporting
convention; this file is the design document and is not a substitute for it.)

Until one exists, report privately through **GitHub private vulnerability
reporting** at <https://github.com/guided-traffic/mosquitto-operator> (Security →
Report a vulnerability), or to the maintainer organisation
<https://github.com/guided-traffic>. Please do **not** open a public issue for a
finding that lets someone read a Secret, take over a broker's configuration, or
reach the operator's ServiceAccount.

What to include: the operator version (`app.kubernetes.io/version` on the
operator pod), the chart version, which install path was used (Helm or
kustomize), whether `spec.tls` was set, and the contents of `spec.config` if the
finding involves it.
