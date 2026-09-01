# ADR 0009: Delete only through owner references, and never patch

## Status

Accepted. Date: 2026-09-01.

**Verified by reading:**
[`internal/controller/mosquitto_controller.go`](../../internal/controller/mosquitto_controller.go)
in full — the `+kubebuilder:rbac` markers, `Reconcile`, `reconcileResources`,
`reconcileConfigMap`, `reconcileService`, `reconcileStatefulSet`, `ensureOwned`,
`SetupWithManager`;
[`config/rbac/role.yaml`](../../config/rbac/role.yaml) and
[`deploy/helm/mosquitto-operator/templates/clusterrole.yaml`](../../deploy/helm/mosquitto-operator/templates/clusterrole.yaml);
the `Namespace:` assignments in [`internal/builder/configmap.go`](../../internal/builder/configmap.go),
[`internal/builder/service.go`](../../internal/builder/service.go) and
[`internal/builder/statefulset.go`](../../internal/builder/statefulset.go);
[`internal/controller/mosquitto_controller_test.go`](../../internal/controller/mosquitto_controller_test.go);
[`test/integration/mosquitto_test.go`](../../test/integration/mosquitto_test.go);
[`test/e2e/mosquitto_test.go`](../../test/e2e/mosquitto_test.go); and
`controllerutil.SetControllerReference` in `sigs.k8s.io/controller-runtime@v0.24.1`
(`pkg/controller/controllerutil/controllerutil.go`), which is where
`BlockOwnerDeletion: true` and `Controller: true` come from.

**Verified by running**, on the tree as of this date:

| Command | Result |
|---|---|
| `grep -rn "\.Delete(" --include='*.go' . \| grep -v _test.go` | no matches outside tests |
| `grep -rn "\.Patch(" --include='*.go' . \| grep -v _test.go` | exactly one match, and it is the comment on `mosquitto_controller.go:61` that says nothing patches |
| `grep -rln "\.Delete(" --include='*.go' .` | `test/e2e/e2e_test.go` only |
| `grep -rln "\.Patch(" --include='*.go' .` | `internal/controller/mosquitto_controller.go` only, same comment |
| `grep -rn "\.List(" --include='*.go' . \| grep -v _test.go` | no matches |
| `grep -rn "Finalizer" --include='*.go' internal/ cmd/ api/` | no matches; the only hit in the repository is a fake finalizer a unit test sets on its own fixture |
| `make verify-rbac-parity` | passes; both install paths render the same eight grants ([ADR 0006](0006-both-install-paths-grant-the-same-authority.md) D6) |

The three `.Delete(` sites in `test/e2e/e2e_test.go` are the E2E harness cleaning up after
itself — two `Namespaces().Delete` calls and the `deleteMosquitto` helper that removes the CR
under test. None of them is operator code, and none of them runs with the operator's
ServiceAccount.

**Not verified.** Nothing in this repository has ever run against a real cluster, so the
central claim — *the garbage collector actually removes these objects* — is verified as
**intent** (the references are set, and asserted to be set) and as **encoded expectation** (the
E2E subtest below), never as an observation. The E2E leg that would observe it exists in the
tree; I did not run it and no run of it is recorded anywhere here.

## Context

The reconciler writes exactly four objects for one `Mosquitto`, all of them in `m.Namespace`:

| Object | Name | Built by |
|---|---|---|
| ConfigMap | `<name>-config` | `builder.BuildConfigMap` |
| Headless Service | `<name>-headless` | `builder.BuildHeadlessService` |
| Client Service | `<name>` | `builder.BuildClientService` |
| StatefulSet | `<name>` | `builder.BuildStatefulSet` |

Two facts about that set drive everything below.

**Every one of those names is derived from the CR name**, so a pre-existing object can already
hold one. `broker-config`, `broker-headless` and `broker` are names a human would plausibly
have used for something else in the same namespace, and the operator meets them through a `Get`
that succeeds.

**The teardown has to happen somehow.** Either the operator deletes the four objects when the CR
goes away, which needs a finalizer to keep the CR alive long enough and `delete` on three
resources in every namespace, or the API server's garbage collector does it from owner
references, which needs neither. The first option puts `delete` on `configmaps`, `services` and
`statefulsets` into a **cluster-wide** ClusterRole — every namespace, every object of those
kinds, for the lifetime of the install — to reproduce a behaviour Kubernetes already implements.

There is a third force, quieter than the other two. A reconcile pass that runs while the CR is
being deleted writes objects the garbage collector is concurrently removing. Nothing serialises
those two: the operator would recreate what the collector just deleted, and the collector would
delete what the operator just recreated, until one of them ran out of work to do. Which one wins
depends on timing, which is the definition of a race.

## Decision

**D1 — Every managed object carries a controller reference, set before it is written.** All
three reconcile helpers call
`controllerutil.SetControllerReference(m, desired, r.Scheme)` on the desired object and wrap a
failure with the object name (`"setting owner reference on ConfigMap %s: %w"` and its Service
and StatefulSet counterparts). Controller-runtime v0.24.1 builds that reference with
`BlockOwnerDeletion: true` and `Controller: true`. The reference is set on the create path,
where `r.Create(ctx, desired)` writes it; on the update path the operator writes `current`,
which already carries the reference from its own creation.

**D2 — No rule grants `delete` on `configmaps`, `services` or `statefulsets`**, because the
owner references make the garbage collector remove them. This is the marker set, verbatim from
`internal/controller/mosquitto_controller.go`:

```go
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update
```

Five verbs each, no sixth. The rendered ClusterRole matches on both install paths
([ADR 0006](0006-both-install-paths-grant-the-same-authority.md) D6). **The operator cannot
delete a ConfigMap, a Service or a StatefulSet anywhere in the cluster, including its own** —
and a capability that does not exist cannot be aimed at the wrong object by a bug.

**D3 — No rule in the ClusterRole grants `patch`.** Nothing in the reconciler patches: every
write is `Create` or `Update`, and the status is `Status().Update()`, which is a PUT. The grep in
the Status section is the check, and its only hit is the comment asserting the property. If
server-side apply is ever adopted, `patch` comes back *with the code that needs it*, not before.

The scope of that sentence is deliberate and the wider claim would be false: the namespaced
leader-election `Role` does grant `patch` — on `coordination.k8s.io/leases` and on `events` — in
both install paths. That is client-go's `LeaseLock` writing the operator's own Lease and the
`LeaderElection` Event, which belongs to the manager rather than to the reconciler and never
touches a managed object. [ADR 0006](0006-both-install-paths-grant-the-same-authority.md) D6
carries the full rendered grant table, leader-election rules included.

**D4 — `list` and `watch` are informer verbs, not call sites, and the markers say so.** No line
of the non-test tree calls `List` — the grep finds none. Those two verbs are what
controller-runtime's cache needs to establish and maintain an informer for a type at all, so
they are required for every kind `SetupWithManager` registers: `Mosquitto` through `For`, and
`StatefulSet`, `ConfigMap` and `Service` through `Owns`. Stating this is not pedantry: an
auditor who assumes every verb maps to a call would try to remove them, and the operator would
fail to start its cache.

**D5 — An object the `Mosquitto` does not control is refused, never adopted.** After a
successful `Get`, each helper calls `ensureOwned(current, m, "<Kind>")`:

```go
func ensureOwned(obj metav1.Object, m *mkov1.Mosquitto, kind string) error {
	if metav1.IsControlledBy(obj, m) {
		return nil
	}
	return fmt.Errorf("%s %s/%s exists and is not owned by this Mosquitto", kind, obj.GetNamespace(), obj.GetName())
}
```

`metav1.IsControlledBy` matches on the **controller** reference and its UID, so an object owned
by a *different* `Mosquitto`, an object owned by nobody, and an object recreated under the same
name after the CR was deleted and recreated all fail the check identically. **The refusal
propagates as a reconcile failure, not as a skip:** the error travels up through
`reconcileResources` into `Reconcile`, which writes `PhaseFailed` with a `Ready` condition of
`ConditionFalse`, reason `ReconcileFailed` and the error text as the message, persists that
status, and returns the error so the work queue backs the retry off. The resource cannot do its
job without the object, so a silent skip would leave a CR that looks healthy and serves nothing.

Adoption is the alternative that was rejected, and the Services are the sharpest case:
`spec.selector` of a live Service is mutable, so — unlike the StatefulSet, whose immutable
fields would make the API server reject the write — nothing at the API level stops the operator
from repointing somebody else's Service at these pods. The ownership check is that stop.

**D6 — A `Mosquitto` with a non-zero `DeletionTimestamp` gets no writes at all.** The check sits
immediately after the initial `Get`, before any object is built:

```go
if !m.DeletionTimestamp.IsZero() {
	logger.Info("Mosquitto resource is being deleted, skipping reconciliation")
	return ctrl.Result{}, nil
}
```

It returns an empty `ctrl.Result` and a nil error, so the key leaves the queue instead of being
retried. This is the answer to the race in the Context: the owner references are already doing
the teardown, and rewriting the objects here would fight the garbage collector for them. The
early return covers the status write too — `updateStatus` is downstream of it — so the operator
also does not spend an API write recording a phase for a resource that is about to stop
existing.

**D7 — The operator registers no finalizer of its own.** It has nothing to do on the way out
that the garbage collector does not already do, and a finalizer with nothing to do is a way to
wedge a namespace when the operator is removed before its CRs are. The `mosquittoes/finalizers`
rule in the ClusterRole is unrelated to this: it exists because the owner references carry
`blockOwnerDeletion`, and the `OwnerReferencesPermissionEnforcement` admission plugin — off by
default, on in some managed distributions — rejects such a reference unless the writer may
update the owner's finalizers. Without that rule the operator would create nothing on those
clusters.

**D8 — The `Mosquitto` CR itself is read-only to the reconciler.** Its rule is
`get;list;watch` — no `create`, no `update`, no `delete`. The only thing the operator writes on
the resource is the status subresource, which has its own rule carrying `update` alone.

## Consequences

* **Teardown is asynchronous and the operator has no say in it.** After the CR is deleted the
  four objects linger until the garbage collector gets to them. The E2E test allows
  `garbageCollectionTimeout = 2 * time.Minute` for that, with a comment stating plainly that
  nothing about it is instant. There is no operator-side progress signal, because the operator
  is no longer looking.
* **PVCs are not part of this and outlive the CR.** With `spec.storage` set,
  `buildVolumeClaimTemplates` renders a `volumeClaimTemplates` entry named `data`; the PVCs are
  then created by the StatefulSet controller, not by this operator. No
  `PersistentVolumeClaimRetentionPolicy` is set anywhere (grep: no match in the tree), and the
  operator holds no `persistentvolumeclaims` rule at all, so **deleting a `Mosquitto` leaves its
  data volumes behind.** For a broker with persistence that is the safer default, but it is a
  default, not an accident, and nobody should discover it from a storage bill.
* **A name collision is a hard stop, and it is loud.** A pre-existing `<name>-config` or
  `<name>` Service parks the resource in `PhaseFailed` with the offending
  `Kind namespace/name exists and is not owned by this Mosquitto` on its `Ready` condition, and
  the pass retries with backoff forever. The operator will not resolve it: an administrator has
  to rename or remove the foreign object. That is the intended cost of not adopting.
* **A CR deleted mid-rollout is not cleaned up in an orderly way.** D6 means the operator stops
  contributing the moment the timestamp is set, so whatever half-written state exists is handed
  to the garbage collector as is. There is no drain, no ordered shutdown and no last status
  update.
* **`delete` and `patch` are unavailable if a future feature needs them.** Adopting server-side
  apply, or cleaning up an object that stops being desired (a Service that a spec change makes
  obsolete, say), means widening a cluster-wide ClusterRole and mirroring it into the chart by
  hand. That is deliberate friction, not an oversight.

## Alternatives Considered

### A finalizer plus explicit deletes

Rejected. It buys ordered teardown and costs `delete` on `configmaps`, `services` and
`statefulsets` **cluster-wide**, plus a finalizer that can wedge a `Mosquitto` — and therefore
its namespace — if the operator is uninstalled before its CRs are removed. Kubernetes already
implements cascading deletion from owner references; reimplementing it here would add authority
and a stuck-state failure mode to get a behaviour that is already available for free.

### Adopt an existing object that matches the name

Rejected, and it is the alternative with the worst failure. Adoption would hand somebody else's
workload or somebody else's traffic to this operator on the strength of a name match: the
Service case silently repoints live traffic at the broker pods, and the object's real owner sees
its Service change under it with no event that explains why. A name is not a claim of ownership.

### Adopt only when the object carries the operator's labels

Rejected as a weaker version of the same mistake. Labels are writable by anyone with `update` in
the namespace, so the test would be "does this object claim to be ours", which is not a
provenance check. `metav1.IsControlledBy` compares the controller reference **and its UID**, so
it survives a delete-and-recreate of the CR under the same name.

### Skip the object and carry on when it is not owned

Rejected. It converts a hard configuration error into a `Mosquitto` that reports progress while
missing its config, its DNS or its pods. The refusal is reported on the resource precisely so it
is visible with `kubectl get` and not only in the operator log.

### Keep reconciling while the CR is being deleted

Rejected: that is the race in the Context. The operator would recreate objects the garbage
collector is removing, and the outcome would depend on which loop ran last.

### Grant `patch` pre-emptively, since server-side apply is the direction of travel

Rejected. A verb granted for a migration that has not happened is a verb nobody is checking, on
every namespace in the cluster. It comes back with the code that uses it.

## Residual risks

* **Garbage collection is unobserved from this repository (open).** The unit tier asserts the
  references are set (`metav1.IsControlledBy` over all four objects in
  `TestReconcile_CreatesEveryManagedObject`) and that a deleting CR produces no objects
  (`TestReconcile_DeletionIsLeftToGarbageCollection`), but a fake client runs no garbage
  collector. The E2E subtest *deleting the CR removes everything it owns* and its
  `waitForOwnedObjectsGone` helper are the only place the collector itself would be exercised,
  and no run of them is recorded here.
* **The integration tier asserts acceptance, never collection (open).** Envtest starts an API
  server and etcd, not `kube-controller-manager`, so there is no garbage collector in it, and a
  grep for `OwnerReference` across `test/` finds no assertion at all. What that tier does verify
  is adjacent and real: `TestIntegration_Reconcile_RefusesAnObjectItDoesNotOwn` exercises the D5
  refusal against a real API server.
  *(This ADR previously said the package comment in `test/integration/suite_test.go` claimed
  otherwise. It did, and it was corrected on 2026-09-01: the comment now lists garbage collection
  under what the tier deliberately cannot answer and points at the E2E subtest that covers it.)*
* **The refusal has no expiry and no escalation (accepted).** A `Mosquitto` blocked on a foreign
  object retries with backoff indefinitely. Nothing raises an alert, because this operator
  emits no Events — it has no `EventRecorder` at all (the manager does serve
  controller-runtime's own metrics on `:8080`, but nothing per-resource)
  ([ADR 0006](0006-both-install-paths-grant-the-same-authority.md) D6). The only signal is the
  CR status and the operator log.
* **Orphaned PVCs are nobody's job (accepted, see Consequences).** No retention policy, no RBAC,
  no documentation-enforced cleanup step in the code.
* **`blockOwnerDeletion` behaviour under `OwnerReferencesPermissionEnforcement` is reasoned, not
  observed (open).** The `mosquittoes/finalizers` rule is present for it, but no cluster with
  that admission plugin enabled has been tested from here.
* **Cross-namespace ownership is impossible and untested here.** Every managed object is built
  with `Namespace: m.Namespace`, and a namespaced owner cannot own an object in another
  namespace, so the question never arises today. A future feature that writes outside the CR
  namespace would silently lose its teardown, because such a reference is ignored by the
  garbage collector rather than rejected.

## References

* [`internal/controller/mosquitto_controller.go`](../../internal/controller/mosquitto_controller.go) — the markers, the `DeletionTimestamp` early return, `ensureOwned`, and the three `SetControllerReference` call sites
* [`config/rbac/role.yaml`](../../config/rbac/role.yaml) — the generated ClusterRole with no `delete` and no `patch`
* [`deploy/helm/mosquitto-operator/templates/clusterrole.yaml`](../../deploy/helm/mosquitto-operator/templates/clusterrole.yaml) — the hand-written mirror of the same rules
* [`internal/builder/statefulset.go`](../../internal/builder/statefulset.go) — `buildVolumeClaimTemplates` and the `data` volume that is not owner-referenced
* [`internal/controller/mosquitto_controller_test.go`](../../internal/controller/mosquitto_controller_test.go) — `TestReconcile_CreatesEveryManagedObject`, `TestReconcile_DeletionIsLeftToGarbageCollection`, `TestReconcile_RefusesForeignObjects`, `TestEnsureOwned`
* [`test/integration/mosquitto_test.go`](../../test/integration/mosquitto_test.go) — `TestIntegration_Reconcile_RefusesAnObjectItDoesNotOwn`
* [`test/e2e/mosquitto_test.go`](../../test/e2e/mosquitto_test.go) — `waitForOwnedObjectsGone` and `garbageCollectionTimeout`
* [ADR 0006](0006-both-install-paths-grant-the-same-authority.md) — the full rendered grant set, and why both install paths have to agree on it
