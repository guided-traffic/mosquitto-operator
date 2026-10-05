# The operator's privilege footprint

What the operator's ServiceAccount is granted, on both install paths, what each grant permits, what
is absent by decision, and how the operator process itself is hardened and exposed. Who the
operator acts for and against is [trust-boundaries.md](trust-boundaries.md); what keeps its writes
inside a resource's namespace is [tenancy.md](tenancy.md).

## Two install paths, one authority

The operator ships two install paths, and they are meant to grant the same thing:
`helm install` from [`deploy/helm/mosquitto-operator`](../../deploy/helm/mosquitto-operator), and
`kustomize build config/default | kubectl apply -f -`
([ADR 0006](../adr/0006-both-install-paths-grant-the-same-authority.md)).

The kustomize path is two commands, not one: `config/default` composes only `../rbac` and
`../manager` and renders six objects — ServiceAccount, Role, ClusterRole, RoleBinding,
ClusterRoleBinding, Deployment — and **no CustomResourceDefinition**. The CRD lives in `config/crd`
and is applied by `make install`. The chart has no such split: `templates/crd.yaml` ships with it.

Only the kustomize path is generated: `make generate-all` runs controller-gen over the
`+kubebuilder:rbac` markers in
[`internal/controller/mosquitto_controller.go`](../../internal/controller/mosquitto_controller.go)
and writes [`config/rbac/role.yaml`](../../config/rbac/role.yaml). The chart's
[`clusterrole.yaml`](../../deploy/helm/mosquitto-operator/templates/clusterrole.yaml) is written by
hand. `make verify-rbac-parity` renders both and compares them as
`(kind, apiGroup, resource) → verb set`
([`test/rbacparity/rbac_parity_test.go`](../../test/rbacparity/rbac_parity_test.go)), and the
`generated-manifests` job runs it on every push to `main` and every pull request. Run for this page
on 2026-10-05 (helm v3.21.3 and kustomize v5.8.1 locally; CI installs helm v4.3.0): **PASS,
"compared 8 grants across both install paths".**

| Object | Helm, release `mosquitto-operator` | kustomize, `config/default` |
|---|---|---|
| ServiceAccount | `mosquitto-operator` (release namespace) | `mosquitto-operator-mosquitto-operator` (`mosquitto-operator-system`) |
| ClusterRole and ClusterRoleBinding | `mosquitto-operator` | `mosquitto-operator-mosquitto-operator-role`, `mosquitto-operator-mosquitto-operator` |
| Role and RoleBinding (leader election) | `mosquitto-operator-leader-election`, only while `leaderElection.enabled` | `mosquitto-operator-mosquitto-operator-leader-election`, always |

## The grants and what each permits

Eight grants, in seven rows, because `configmaps` and `services` carry an identical rule and
controller-gen emits them as one:

| Kind | API group | Resources | Verbs | What it permits |
|---|---|---|---|---|
| ClusterRole | `mko.gtrfc.com` | `mosquittoes` | get, list, watch | Read every `Mosquitto` in the cluster. **No `create`, `update` or `delete`** — the reconciler never writes a resource's spec ([ADR 0009](../adr/0009-delete-only-through-owner-references.md) D8) |
| ClusterRole | `mko.gtrfc.com` | `mosquittoes/status` | update | Status authority. `update` alone, because `Status().Update()` is a PUT and nothing reads status on its own |
| ClusterRole | `mko.gtrfc.com` | `mosquittoes/finalizers` | update | The owner references the reconciler writes carry `blockOwnerDeletion`, and the `OwnerReferencesPermissionEnforcement` admission plugin — off by default, on in some managed distributions — refuses such a reference unless the writer may update the owner's finalizers. Per the marker comment; not observed on such a cluster |
| ClusterRole | `""` (core) | `configmaps`, `services` | create, get, list, update, watch | **Create or overwrite any ConfigMap or Service in any namespace.** Overwriting a Service's `spec.selector` redirects its traffic; overwriting a ConfigMap changes what its consumers read |
| ClusterRole | `apps` | `statefulsets` | create, get, list, update, watch | **Create a StatefulSet, or replace the pod template of any existing one, in any namespace** — the image, the command, the volumes and the ServiceAccount its pods run as. The heaviest grant here ([H-5](#h-5)) |
| Role (operator namespace) | `coordination.k8s.io` | `leases` | create, delete, get, list, patch, update, watch | Leader election. Namespaced on purpose: the Lease `mosquitto-operator.mko.gtrfc.com` (`LeaderElectionID` in [`cmd/main.go`](../../cmd/main.go)) lives in the operator's own namespace |
| Role (operator namespace) | `""` (core) | `events` | create, patch | client-go's `LeaseLock` records a `LeaderElection` Event when leadership changes. The reconciler records no Events |

## What is absent

Absent by decision rather than by oversight
([ADR 0009](../adr/0009-delete-only-through-owner-references.md) D2–D4):

- **No `delete` and no `patch` in the ClusterRole.** Teardown is the garbage collector's job
  through the owner references, and every write is `Create` or `Update`: a grep for `.Patch(` and
  `.Delete(` over the non-test Go tree returns only the comment that says so. A capability that
  does not exist cannot be aimed at the wrong object by a bug.
- **The exception, stated so the sentence above stays true:** the namespaced leader-election Role
  grants `delete` and `patch` on `coordination.k8s.io/leases` and `patch` on `events`. That is
  client-go's `LeaseLock` operating on the operator's own Lease in its own namespace, not the
  reconciler reaching a managed object. Both install paths render it identically at default values.
- **No rule on `secrets`.** Not narrowed — absent, and the chart says so in a comment rather than
  by omission. The kubelet mounts the TLS material; the operator never reads it
  ([credentials.md](credentials.md#the-operator-holds-no-workload-credential)).
- **Nothing on `rbac.authorization.k8s.io`, no `escalate`, no `bind`, no `serviceaccounts`.** The
  operator creates no per-instance identity, so it needs no authority to grant one.
- `list` and `watch` are informer verbs, not call sites: controller-runtime's cache needs them for
  every kind the manager watches, and no line of the non-test tree calls `List`.

**In one paragraph.** This is a workload manager with cluster-wide create and update on three
kinds and read on its own CRD. Through the API it cannot read a Secret, cannot delete any object,
and cannot write RBAC. That is not a bound on what its identity reaches: `statefulsets: create,
update` in every namespace is the authority to run chosen code under any ServiceAccount, with any
Secret of the namespace mounted, wherever the namespace's admission lets the pod in. Treat its
ServiceAccount token and its image accordingly ([H-5](#h-5)).

## What the parity test does not cover

The test compares RBAC, at **chart default values**, over `config/default`:

- **With `leaderElection.enabled=false` the chart renders no Role at all** and passes no
  `--leader-elect`, while `config/default` always includes
  [`config/rbac/leader_election_role.yaml`](../../config/rbac/leader_election_role.yaml) and
  [`config/manager/manager.yaml`](../../config/manager/manager.yaml) always passes `--leader-elect`.
  Rendered on 2026-10-05: zero Role documents. The parity statement is "the two paths agree at
  default values", not "the chart cannot be configured into a different shape".
- **The metrics port is switchable in one path only.** The chart renders
  `--metrics-bind-address=:8080`, or `=0` under `metrics.enabled=false` — `0` is the literal
  controller-runtime reads as "do not start the metrics server" (rendered on 2026-10-05).
  `manager.yaml` passes no `--metrics-bind-address`, so the kustomize path always takes the default
  `:8080` from [`cmd/main.go`](../../cmd/main.go) and changes only by editing the manifest.
- **`make install` applies `config/rbac` without the `config/default` overlay**, so the objects it
  creates carry no name prefix and the namespaced ones — the ServiceAccount, the Role and the
  RoleBinding — are addressed to the literal namespace `system`. The parity test never renders that
  path ([ADR 0006](../adr/0006-both-install-paths-grant-the-same-authority.md), residual risks).

## The operator process

Identical hardening on both paths, and the comment in
[`config/manager/manager.yaml`](../../config/manager/manager.yaml) says that is deliberate. Pod
level: `runAsNonRoot: true`, `seccompProfile: RuntimeDefault`, `terminationGracePeriodSeconds: 10`.
Container level: `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true`,
`capabilities: drop [ALL]`. The image is `gcr.io/distroless/static-debian12:nonroot` with one
binary built `CGO_ENABLED=0` ([`Containerfile`](../../Containerfile)), so the container has no
shell. The ServiceAccount token is mounted — the manager needs it for every API call — and it is
the credential [H-5](#h-5) is about.

Two ports, both plain HTTP: `:8080` metrics and `:8081` health (`/healthz` and `/readyz`, both
`healthz.Ping`). `managerOptions` passes only `BindAddress` to controller-runtime's metrics server —
no `SecureServing`, no `FilterProvider` — so no authentication or authorization filter applies
([`cmd/main.go`](../../cmd/main.go); controller-runtime v0.24.1 `pkg/metrics/server`).

What `:8080` discloses is controller-runtime's own registry. **This repository registers no
collector of its own**: no `prometheus` import exists in `internal/`, `cmd/` or `api/`, and
`github.com/prometheus/client_golang` is an indirect dependency in [`go.mod`](../../go.mod). Which
series that registry exposes on this version was not measured; what the code shows is that nothing
this repository wrote publishes a `Mosquitto` name, a namespace, an image or spec content there.

## What this does not cover

<a id="h-4"></a>
### H-4 — The operator's metrics endpoint answers anyone who can route to it

Live by default: `metrics.enabled` defaults to `true`, and the kustomize path has no switch at all.
Anything that can route to the operator pod reads `:8080/metrics` unauthenticated, in plain HTTP,
with or without the chart's optional `<fullname>-metrics` Service — that Service adds a DNS name and
a selector, not reach. What it discloses is described under *The operator process* above, and was
not measured. What a cluster operator can do: `metrics.enabled=false`, which passes
`--metrics-bind-address=0` and closes the port; or a NetworkPolicy restricting ingress to the
operator pod to the monitoring namespace ([tenancy.md H-6](tenancy.md#h-6)). Deleting the
`-metrics` Service only removes a name.

<a id="h-5"></a>
### H-5 — The operator's identity can rewrite every ConfigMap, Service and StatefulSet in the cluster

Live. Whoever holds the operator's ServiceAccount token, or can change the operator's image, can
create or overwrite a ConfigMap, a Service or a StatefulSet in every namespace. Replacing a pod
template is replacing the code that runs; a pod template also names the ServiceAccount its pods run
as and the Secrets they mount, so through the StatefulSet grant this identity reaches every Secret
and every workload identity of every namespace whose admission lets the pod it writes in — on a
cluster where some ServiceAccount holds `cluster-admin`, it is one pod away from it. Pod Security
admission does not stand in the way: it judges a pod's security fields, not which ServiceAccount it
runs as or which Secret it mounts.
Through the API it cannot delete, cannot patch and cannot read a Secret; that limits a buggy
reconciler, not a holder of the token. `ensureOwned` does not help here: it guards the reconcile
path, and a stolen token is not subject to it ([tenancy.md](tenancy.md#what-holds)).

Narrowing the ClusterRole to the namespaces actually served would cost the install-and-forget
property for new namespaces; it has not been done, and it is an open trade
([ADR 0006](../adr/0006-both-install-paths-grant-the-same-authority.md), alternatives). What a
cluster operator can do meanwhile: treat the operator's namespace, its ServiceAccount token and its
image as cluster-admin-adjacent — restrict who may create pods in, exec into, or change workloads
of the operator's namespace, and who may change the operator's image; an admission policy of the
cluster's own that limits what this ServiceAccount may write is the only control that bounds the
grant itself, and none ships here.
