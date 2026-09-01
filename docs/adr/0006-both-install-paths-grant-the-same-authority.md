# ADR 0006: Both install paths grant the same authority, and a test compares what they render

## Status

Accepted. Date: 2026-09-01.

**Verified by reading:** the `+kubebuilder:rbac` markers and the comment above them in
[`internal/controller/mosquitto_controller.go`](../../internal/controller/mosquitto_controller.go);
[`config/rbac/role.yaml`](../../config/rbac/role.yaml),
[`config/rbac/leader_election_role.yaml`](../../config/rbac/leader_election_role.yaml),
[`config/rbac/leader_election_role_binding.yaml`](../../config/rbac/leader_election_role_binding.yaml),
[`config/rbac/role_binding.yaml`](../../config/rbac/role_binding.yaml),
[`config/rbac/kustomization.yaml`](../../config/rbac/kustomization.yaml),
[`config/default/kustomization.yaml`](../../config/default/kustomization.yaml),
[`config/manager/manager.yaml`](../../config/manager/manager.yaml);
[`deploy/helm/mosquitto-operator/templates/clusterrole.yaml`](../../deploy/helm/mosquitto-operator/templates/clusterrole.yaml),
[`deploy/helm/mosquitto-operator/templates/leader-election.yaml`](../../deploy/helm/mosquitto-operator/templates/leader-election.yaml),
[`deploy/helm/mosquitto-operator/templates/deployment.yaml`](../../deploy/helm/mosquitto-operator/templates/deployment.yaml),
[`deploy/helm/mosquitto-operator/values.yaml`](../../deploy/helm/mosquitto-operator/values.yaml);
[`test/rbacparity/rbac_parity_test.go`](../../test/rbacparity/rbac_parity_test.go);
the `manifests`, `generate-all`, `sync-helm-crd`, `install`, `deploy` and `verify-rbac-parity`
targets in [`Makefile`](../../Makefile); and the `generated-manifests` job in
[`.github/workflows/release.yml`](../../.github/workflows/release.yml).

**Verified by running:** `make verify-rbac-parity` passes, and
`go test -tags=rbacparity -count=1 -v ./test/rbacparity/...` logs
`compared 8 grants across both install paths`. The grant table in D6 was produced by rendering
both paths and collapsing them with the same `(kind, apiGroup, resource)` key the test uses,
not by transcribing the YAML by eye.

**Not verified.** Neither install path has ever been applied to a real cluster from this
repository — no such run is recorded anywhere in the tree. The test therefore proves the two
paths are *equal*, and nothing here proves either one is *sufficient*: an operator that 403s at
runtime would 403 identically on both. The pre-fix state of the defect in the Context is also
not recoverable from git history — the repository has two commits and the corrected files are
already in the second — so that defect is recorded from the maintainer's report of 2026-09-01
and cannot be re-derived from the tree.

## Context

The operator ships two install paths, and both are advertised:

* `helm install` from [`deploy/helm/mosquitto-operator`](../../deploy/helm/mosquitto-operator);
* `kustomize build config/default | kubectl apply -f -`, which is what `make deploy` runs.

**Only one of them is generated.** `make manifests` runs
`controller-gen rbac:roleName=mosquitto-operator-role crd paths="./..."
output:crd:artifacts:config=config/crd/bases output:rbac:artifacts:config=config/rbac`, so
`config/rbac/role.yaml` is a build product of the `+kubebuilder:rbac` markers on
`MosquittoReconciler`. `make generate-all` is `manifests generate sync-helm-crd`, and
`sync-helm-crd` copies `config/crd/bases/*.yaml` into
`deploy/helm/mosquitto-operator/templates/crd.yaml` — **the CRD is the only generated artifact
that crosses into the chart.** The chart's `templates/clusterrole.yaml` is written by hand and
nothing regenerates it.

That asymmetry has a failure mode with no natural alarm. A marker change updates
`config/rbac/role.yaml` on the next `make generate-all` and leaves the chart behind, and the
consequence is bad in both directions:

* **too few verbs in the chart** — the operator 403s on every pass, for chart users only;
* **too many** — chart users hand out cluster-wide authority the code no longer asks for.

Neither shows up anywhere else. These are manifests: there is no compilation step to break, no
type to fail, and the operator's own unit and integration tiers never read either file.

### The defect that made this a decision

Found 2026-09-01, before any of this was released. The two install paths granted **different
authority for leader election**, and nobody had decided that:

* the **chart** granted `coordination.k8s.io/leases` in its **ClusterRole** — cluster-wide,
  every Lease in every namespace — and the verb set included `delete`;
* `config/rbac` granted the same resource through a **namespaced `Role`** in the operator's own
  namespace;
* the chart was additionally **missing the `events` rule** that
  `config/rbac/leader_election_role.yaml` carried.

The Lease the manager actually takes is a single object. `cmd/main.go` sets
`LeaderElectionID: "mosquitto-operator.mko.gtrfc.com"` and leaves `LeaderElectionNamespace`
unset; controller-runtime v0.24.1 then defaults the resource lock to
`resourcelock.LeasesResourceLock` and the namespace to `getInClusterNamespace()`
(`pkg/leaderelection/leader_election.go`), which is the namespace the operator pod runs in. One
Lease, one namespace — so the chart was granting reach over every Lease in the cluster to buy
nothing, and could additionally delete any of them.

Both are namespaced now, and the chart gained the missing `events` rule.

### Why the comparison key includes the kind

**The same verbs on a `Role` and on a `ClusterRole` are not the same authority.** That is
exactly what the defect was: the verb sets were close enough that a comparison ignoring the kind
would have called them equal while one path granted the namespace and the other granted the
cluster. So the test keys on `(kind, apiGroup, resource)`, with `kind` taken from the decoded
document and kept in the key:

```go
type grant struct {
	kind     string
	apiGroup string
	resource string
}
```

The object *name* is deliberately not in the key, because the two paths are not supposed to
agree on names: `config/default` applies `namePrefix: mosquitto-operator-` and renders
`mosquitto-operator-mosquitto-operator-role`, while the chart derives its name from the Helm
release through `mosquitto-operator.fullname` (rendered as `parity-mosquitto-operator` when the
test templates the release `parity`). Comparing names would compare the naming schemes, not the
authority.

## Decision

**D1 — Two install paths are supported, and both are first class.** Helm for people who install
charts, `kustomize build config/default` for people who do not want a chart in the loop.
Neither is a second-class copy of the other, which is precisely why their authority has to be
argued rather than assumed.

**D2 — The kubebuilder markers are the single source of intent, `config/rbac/role.yaml` is
generated from them, and the chart's ClusterRole mirrors them by hand.** The markers live above
`Reconcile` in `internal/controller/mosquitto_controller.go` with a comment justifying every
verb; `make generate-all` regenerates `config/rbac/role.yaml`; a marker change is not finished
until `deploy/helm/mosquitto-operator/templates/clusterrole.yaml` carries the same rule.

**D3 — Parity is asserted over the RENDERED output of both paths, not over the source files.**
`TestRBACParity_BothInstallPathsGrantTheSameAuthority` shells out to
`helm template parity deploy/helm/mosquitto-operator --namespace mosquitto-operator-system` and
`kustomize build config/default`, then decodes every `ClusterRole` and `Role` in each stream
into `rbacv1.ClusterRole` (the two kinds carry an identical `Rules` field, so one type decodes
both, with the kind kept separately). Rule order, `apiGroups` grouped or split across rules,
verb order and each path's name prefixes all wash out. Comparing the source YAML as text would
compare formatting.

**D4 — The comparison key is `(kind, apiGroup, resource)` mapped to a sorted verb set**, and
the kind stays in the key for the reason in the Context. A grant present on one side and absent
on the other is reported by name and direction; a grant present on both with different verbs
fails on the verb set.

**D5 — Leader election is a namespaced `Role` on both paths, and never enters the ClusterRole.**
`config/rbac/leader_election_role.yaml` is a `Role`, `deploy/helm/mosquitto-operator/templates/leader-election.yaml`
renders a `Role` plus its `RoleBinding` in `.Release.Namespace`, and the chart's ClusterRole
carries an explicit comment saying the leader-election rule is left out on purpose. The rules
are not generated on either path: leader election belongs to the manager, not to the reconciler,
so no kubebuilder marker produces them and anything added to `config/rbac/role.yaml` by hand
would be overwritten by the next `make generate-all`.

**D6 — The authority both paths grant is exactly this, and nothing else.** Rendered
2026-09-01 from the two commands in D3, with the chart at its default values:

| Kind | apiGroup | Resource | Verbs |
|---|---|---|---|
| ClusterRole | `apps` | `statefulsets` | `create, get, list, update, watch` |
| ClusterRole | core (`""`) | `configmaps` | `create, get, list, update, watch` |
| ClusterRole | core (`""`) | `services` | `create, get, list, update, watch` |
| ClusterRole | `mko.gtrfc.com` | `mosquittoes` | `get, list, watch` |
| ClusterRole | `mko.gtrfc.com` | `mosquittoes/finalizers` | `update` |
| ClusterRole | `mko.gtrfc.com` | `mosquittoes/status` | `update` |
| Role | `coordination.k8s.io` | `leases` | `create, delete, get, list, patch, update, watch` |
| Role | core (`""`) | `events` | `create, patch` |

Eight grants, identical on both sides — the count the test logs. Three properties of that table
are decisions, not accidents, and each is stated where it is enforced:

* **No `delete` and no `patch` anywhere in the ClusterRole.** Teardown is the garbage
  collector's job through the owner references, and nothing in the reconciler patches — see
  [ADR 0009](0009-delete-only-through-owner-references.md).
* **No `secrets` rule at all.** `spec.tls.secretName` is mounted into the broker pods by the
  kubelet, so the operator never reads the TLS material itself.
* **The only `events` grant is namespaced**, and it exists for client-go, not for the operator:
  `LeaseLock.RecordEvent` in `k8s.io/client-go@v0.37.0/tools/leaderelection/resourcelock/leaselock.go`
  records a `LeaderElection` Event whose subject is the Lease, and controller-runtime wires an
  `EventRecorder` into that lock. The reconciler itself has no `EventRecorder` — a grep for
  `EventRecorder` over `internal/`, `cmd/` and `api/` finds nothing — so the operator emits no
  Events of its own.

**D7 — The check runs in CI on every push and pull request**, as the last step of the
`generated-manifests` job in `.github/workflows/release.yml`, after the job has run
`make generate-all` and failed on a dirty tree. The generator keeps `config/rbac/role.yaml`
honest against the markers; this step is the only thing that keeps the chart honest against
either. The job installs Helm (`azure/setup-helm@v5`, `v4.2.4`) for exactly this step, and
`make verify-rbac-parity` has a `kustomize` prerequisite that downloads the binary into `bin/`
(gitignored, and downloaded after the dirty check, so it cannot dirty the tree it is checked
against).

**D8 — Parity is about authority, not about the whole install.** The two paths are deliberately
not identical elsewhere and the test does not pretend otherwise: the chart renders the CRD and
a metrics `Service`, `kustomize build config/default` renders neither (`config/default` composes
`../rbac` and `../manager` only, and the CRD is applied separately by `make install` from
`config/crd`). Only `ClusterRole` and `Role` documents are compared; everything else in either
stream is skipped after a probe decode that reads no further than `kind`.

## Consequences

* **The chart's ClusterRole is maintained by hand, forever.** Every marker change is two edits
  and a test run instead of one edit and a generator. That cost is real and recurring, and it is
  paid deliberately: see the Alternatives.
* **The safety net only exists when someone runs it.** The test is behind the `rbacparity`
  build tag because it shells out to `helm` and `kustomize`, which the plain unit tier must not
  require, so `make test-unit` will never catch this. Locally it takes `make verify-rbac-parity`;
  in CI it takes the `generated-manifests` job.
* **A developer without `helm` or `kustomize` on PATH gets a failure, not a skip.** The test
  fails with the stderr of whichever command is missing (`kustomize` is looked up in `bin/`
  first, then on PATH, with a `run make kustomize` hint; `helm` is taken from PATH with no
  fallback). Noisy for the wrong reason is the deliberate trade against silently passing.
* **The test proves sameness, not correctness.** Both paths can be equally over-privileged or
  equally short of a verb and this test is green. Nothing in this repository has been applied to
  a real cluster, so the grant table in D6 is verified as *rendered*, not as *sufficient at
  runtime*.
* **The chart is compared at its default values only.** `helm template` is invoked with no
  `--set` and no `-f`, so the comparison describes `values.yaml` as committed —
  `leaderElection.enabled: true`. A user who installs with `leaderElection.enabled=false` gets
  no leader-election `Role` and no `--leader-elect` flag, which is coherent, but it is a
  rendering this test never looks at.
* **A false sense of symmetry is available to a careless reader.** The kustomize path passes
  `--leader-elect` unconditionally in `config/manager/manager.yaml`, while the chart passes it
  only under `leaderElection.enabled`. The RBAC matches in both cases; the *behaviour* does not,
  and no test compares behaviour.

## Alternatives Considered

### Generate the chart ClusterRole from `config/rbac/role.yaml`, the way `sync-helm-crd` handles the CRD

The obvious answer, and the one that would remove the recurring cost — not taken *yet*, and the
obstacles are readable in the tree rather than hypothetical. The chart's ClusterRole is not a
copy of the generated file: its `metadata.name` comes from `mosquitto-operator.fullname`, its
labels from `mosquitto-operator.labels`, and every rule carries a comment explaining what the
verbs are for. A `cat`-style sync like `sync-helm-crd` would destroy all three, so a generator
would have to re-template the output, and it would still not cover the leader-election Role,
which is gated on a chart value and has no marker to generate from. The decision is to pay the
hand-mirroring cost and buy the *detection* instead of the *generation*; the generator stays on
the table (Residual risks).

### Ship only one install path

Rejected as a user-facing regression for the sake of a maintenance problem. Both audiences are
real, and dropping the chart would not remove the class of defect anyway — it would move it into
whatever the remaining path stopped checking.

### Diff the two source files as text

Rejected: it compares formatting. `config/rbac/role.yaml` groups `configmaps` and `services`
into one rule with alphabetically sorted verbs because that is what controller-gen emits; the
chart lists them with comments in between and in a different verb order. Every one of those
differences is meaningless, and a check that fires on meaningless differences gets silenced.

### Key the comparison on `(apiGroup, resource)` only

Rejected, and it is the specific alternative that would have *missed the defect this ADR
exists for*: the chart's cluster-wide leases grant and the kustomize namespaced one would have
collapsed onto one key, leaving only the verb difference to notice — and a check that says
"the chart has an extra `delete`" tells you nothing about the part that mattered, which is that
one of them was cluster-wide.

### Keep leader election in the chart's ClusterRole and add the same ClusterRole to `config/rbac`

Rejected. It would have made the two paths agree by widening the narrow one: cluster-wide
authority over every Lease in the cluster, to serve one Lease in one namespace. Parity is worth
having in the direction of least authority, not most.

### Grant the reconciler's rules namespaced instead of cluster-wide

Not taken, and not free: the operator watches `Mosquitto` resources cluster-wide, so the
ClusterRole is what lets it serve a CR in any namespace. Narrowing it to a namespace list is a
different product decision — it would require the install to know every namespace in advance —
and it is not what either install path does today.

### A comment in both files instead of a test

That is what existed. Both files carry an accurate comment telling the reader to mirror a marker
change by hand, and the defect happened anyway. Comments do not fail a build.

## Residual risks

* **Sufficiency is unproven (open).** Nothing in this repository has run either install path
  against a real API server. Both paths render the same eight grants; whether those eight are
  the set the operator needs at runtime has been argued from the code, not observed.
* **The `leases` rule grants `delete`, and nothing uses it (open, accepted for now).** It is the
  kubebuilder scaffold's verb set, carried on both paths. A grep for `Delete(` over
  `k8s.io/client-go@v0.37.0/tools/leaderelection/` returns nothing — the LeaseLock releases
  leadership by *updating* the Lease with an empty holder identity, never by deleting it. By
  this ADR's own standard an unused verb is authority nobody chose; it is namespaced rather than
  cluster-wide, which is why it is on this list instead of fixed in the same change.
* **`make install` renders `config/rbac` without the `config/default` overlay (not covered).**
  The verbs are the same objects, but the names carry no `mosquitto-operator-` prefix and the
  `Role`/`RoleBinding` land in the literal namespace `system`. The parity test compares
  `config/default` only, so this rendering is checked by nothing.
* **A stale comment contradicted the current design; it was fixed after this ADR was drafted.**
  `config/rbac/leader_election_role.yaml` used to say "The Helm chart grants the same leases
  access through its ClusterRole instead, guarded by `leaderElection.enabled`" — true before the
  chart moved to a namespaced Role, false after. **Fixed 2026-09-01**: the comment now states that the chart mirrors
  the rule as a namespaced `Role` in
  [`templates/leader-election.yaml`](../../deploy/helm/mosquitto-operator/templates/leader-election.yaml)
  and describes the cluster-wide grant as historical. Kept here as a record of the drift, not as
  an open item.
* **The parity test's negative case lives in an ADR, not in the tree (accepted).** The failure
  path is verified by execution, not by reading — [ADR 0010](0010-a-check-is-not-a-check-until-it-has-failed-on-purpose.md)
  records both breaks, and both were reproduced against scratch copies of `HEAD` on 2026-09-01:
  injecting a single `delete` verb into the chart's statefulsets rule fails with
  `ClusterRole apps/statefulsets: the two install paths grant different verbs`, and removing the
  chart's RBAC templates trips the `require.NotEmpty` guard at `rbac_parity_test.go:160`. What is
  still open is that neither break is an automated test, so nothing re-proves it on every run.
* **The chart is only ever compared at default values (open, see Consequences).**

## References

* [`internal/controller/mosquitto_controller.go`](../../internal/controller/mosquitto_controller.go) — the `+kubebuilder:rbac` markers and the comment justifying each verb
* [`config/rbac/role.yaml`](../../config/rbac/role.yaml) — generated by `make manifests` with `rbac:roleName=mosquitto-operator-role`
* [`config/rbac/leader_election_role.yaml`](../../config/rbac/leader_election_role.yaml), [`config/rbac/leader_election_role_binding.yaml`](../../config/rbac/leader_election_role_binding.yaml) — the namespaced leader-election half of the kustomize path
* [`config/default/kustomization.yaml`](../../config/default/kustomization.yaml) — the overlay the parity test renders
* [`deploy/helm/mosquitto-operator/templates/clusterrole.yaml`](../../deploy/helm/mosquitto-operator/templates/clusterrole.yaml) — the hand-written mirror
* [`deploy/helm/mosquitto-operator/templates/leader-election.yaml`](../../deploy/helm/mosquitto-operator/templates/leader-election.yaml) — the chart's namespaced `Role`, gated on `leaderElection.enabled`
* [`test/rbacparity/rbac_parity_test.go`](../../test/rbacparity/rbac_parity_test.go) — `grant`, `authority`, `TestRBACParity_BothInstallPathsGrantTheSameAuthority`
* [`Makefile`](../../Makefile) — `manifests`, `generate-all`, `sync-helm-crd`, `verify-rbac-parity`
* [`.github/workflows/release.yml`](../../.github/workflows/release.yml) — the `generated-manifests` job
* [`cmd/main.go`](../../cmd/main.go) — `LeaderElectionID`, and the `--leader-elect` flag both install paths drive
* [ADR 0009](0009-delete-only-through-owner-references.md) — why the ClusterRole in D6 has no `delete` and no `patch`
