# Adding things

Ordered checklists for the changes that recur. Each names the files to touch and the page to
update in the same change. Read against the tree on 2026-10-05.

## A CRD field

1. Edit [`api/v1/mosquitto_types.go`](../../api/v1/mosquitto_types.go). Give the field kubebuilder
   validation markers and a doc comment that says **what it does now**, not what a later version
   might do. If it has an on/off notion, add a helper next to `IsTLSEnabled` / `IsStorageEnabled`
   that treats a half-filled value as off.
2. If the field changes the security posture, say so in the doc comment. `MosquittoTLS.SecretName`
   is the model: it names both ways of filling the Secret and states plainly that the operator does
   not watch it.
3. Run `make generate-all`. It rewrites `api/v1/zz_generated.deepcopy.go`,
   `config/crd/bases/mko.gtrfc.com_mosquittoes.yaml` and
   `deploy/helm/mosquitto-operator/templates/crd.yaml` (and `config/rbac/role.yaml` when a marker
   changed) ([build-test-lint.md](build-test-lint.md#generated-code)).
4. Consume the field in [`internal/builder`](../../internal/builder). If it affects the pod, make
   sure it lands **inside the pod spec** `buildPodSpec` returns — the pod-spec hash digests exactly
   that, and a field outside it neither rolls the pods nor is compared by `StatefulSetHasChanged`
   ([architecture.md](architecture.md#what-each-write-compares)).
5. Unit-test the builder change. Test defaulting and validation in
   [`test/integration/crd_validation_test.go`](../../test/integration/crd_validation_test.go) — the
   only tier where the markers are enforced by an API server.
6. If the field changes what the broker does at runtime, add an E2E scenario ([below](#an-e2e-scenario)).
7. Add it to the CRD reference in [README.md](../../README.md), with a `# default` or `# example`
   marker on the shown value; update [architecture.md](architecture.md#how-a-change-reaches-a-running-broker)
   if it changes what rolls.
8. Commit the generated files. The `generated-manifests` job regenerates and fails on a dirty tree,
   untracked files included.

## A managed object

1. A new file in [`internal/builder`](../../internal/builder) with a `BuildX(m *mkov1.Mosquitto) *T`
   function. Keep it pure: no client, no context.
2. Give the object a name helper in [`internal/common/labels.go`](../../internal/common/labels.go)
   next to `StatefulSetName` and the Service names — `builder.ConfigMapName` is the one existing name
   that lives outside it — and label it with `common.BaseLabels(m, ResolveImage(m))`, never an
   ad-hoc map.
3. Unit-test it. The existing builder tests are the shape: assert the fields you set, and the
   fields you deliberately did not.
4. Add a `reconcileX` in
   [`internal/controller/mosquitto_controller.go`](../../internal/controller/mosquitto_controller.go)
   with the existing shape — build, `SetControllerReference`, `Get`, `Create` on NotFound,
   `ensureOwned`, then a semantic diff and `Update` — and call it from `reconcileResources` in
   dependency order. `ensureOwned` is not optional for a new kind
   ([ADR 0009](../adr/0009-delete-only-through-owner-references.md)).
5. Diff only the fields the operator owns. If the API server defaults fields on the type, compare a
   hash or a field subset, never the whole object — otherwise every pass reports drift.
6. Add the `+kubebuilder:rbac` marker, **with a comment justifying every verb**: the role is
   cluster-wide. No `delete` (owner references do that) and no `patch` (nothing here patches).
7. `make generate-all`, then mirror the new rule into
   [`deploy/helm/mosquitto-operator/templates/clusterrole.yaml`](../../deploy/helm/mosquitto-operator/templates/clusterrole.yaml)
   **by hand**, then `make verify-rbac-parity`. Too few verbs in the chart and only chart users 403;
   too many and the chart hands out authority the code never asked for — neither direction breaks a
   build ([ADR 0006](../adr/0006-both-install-paths-grant-the-same-authority.md)). Update D6 of that
   ADR, which lists the grants.
8. Add `Owns(&T{})` in `SetupWithManager` if drift on the object should wake the operator.
9. Extend `waitForOwnedObjectsGone` in
   [`test/e2e/mosquitto_test.go`](../../test/e2e/mosquitto_test.go), so the deletion subtest proves
   the new object is collected — the only place garbage collection is observable.
10. Update [architecture.md](architecture.md#what-one-pass-writes), the names table in
    [README.md](../../README.md), and the privilege page under [docs/security/](../security/README.md).

## An E2E scenario

1. A new file in [`test/e2e`](../../test/e2e) starting with `//go:build e2e` and `package e2e`.
2. Name it `TestE2E_<Area>_<Behaviour>` and call `t.Parallel()`.
3. `tc := newTestClients(t)`, then `defer tc.createNamespace(t, "e2e-<something>")()` — or keep the
   returned cleanup and `defer` it. The `e2e-` prefix is not cosmetic: the CI failure collection
   gathers pods, events and logs from namespaces matching `^e2e-`, and the cleanup keeps the
   namespace when the test failed so that evidence is still there.
4. Address the API the way a user does: the literals `mko.gtrfc.com/v1`, `Mosquitto`,
   `mosquittoes`, the label keys, the ports. **Do not import `internal/`.** Use
   `testimages.Default()` for the broker image so `E2E_MOSQUITTO_IMAGE` keeps working.
5. Assert something only a real cluster can answer. The readiness probe is a TCP connect, so the
   existing suite publishes and subscribes with `mosquitto_pub` / `mosquitto_sub` inside the pod.
   If you rely on another binary from the broker image, add it to `clientTools` in
   [`test/imagetools/image_tools_test.go`](../../test/imagetools/image_tools_test.go).
6. Never assert on `secret.Data` — a red run would print a private key into the job log. Assert on
   the key set, as `TestE2E_TLS_CertManagerIssuedSecretServesMQTTS` does.
7. If the scenario depends on the node count, guard it with `tc.requireThreeSchedulableNodes(t)`;
   `E2E_REQUIRE_MULTI_NODE=true` turns its skip into a failure on the multi-node leg.
8. If you rename a test the multi-node leg's `run_filter` selects, update the guard grep in the
   `Run E2E tests` step of [`release.yml`](../../.github/workflows/release.yml) — currently inside
   the commented-out block ([ci-and-release.md](ci-and-release.md#the-e2e-jobs-are-commented-out)).
9. Run it: `make e2e-local KIND_WORKERS=0` for the single-node leg, the multi-node invocation in
   [testing.md](testing.md#the-two-legs) for the other. Add the scenario to the table in
   [testing.md](testing.md#e2e-tests).

## A Renovate-managed pin

1. Put the pin in exactly one place if you can. Where it must exist twice — like the broker image,
   `builder.DefaultImage` and `testimages.MosquittoImage` — add a test that asserts the two are
   equal (`TestPinnedImageIsTheOperatorDefault`) and let one customManager cover both files, so they
   move in one PR.
2. Write the Renovate comment directly above the pin, in a form an existing manager matches:
   `# renovate: datasource=go depName=<module path>` above a `TOOL_VERSION ?= vX.Y.Z` line in the
   Makefile (the regex requires a `v`-prefixed value), or
   `// renovate: datasource=docker depName=<image>` above a Go constant `Name = "<image>:<tag>"`.
3. If no customManager in [`renovate.json`](../../renovate.json) covers the file, add one.
   `managerFilePatterns` must be a `/regex/` or a literal path — `hack/verify-ci-references.mjs`
   rejects globs rather than passing them. Keep `matchStrings` to plain regex constructs: Renovate
   evaluates them with RE2, which rejects lookaround and backreferences Node accepts, and the
   verifier cannot catch that difference.
4. Add a `packageRule` if the pin must stay on a line (`allowedVersions: "<3"` keeps
   `eclipse-mosquitto` on 2.x) or move with others (`groupName: "Go version"`). A new place that
   names the Go version arrives with its own customManager in the same change
   ([ADR 0003](../adr/0003-the-go-version-is-one-fact-in-four-files.md)).
5. Run `make verify-ci-references`. It prints one `OK:` or `BAD:` line per manager with every
   selected file and its match count; zero selected files or a `matchString` that matches nowhere is
   a failure, not a warning.
6. Break it on purpose — the failure the guard exists for, not any failure — watch it fail, restore
   it, and record the exact message in
   [ADR 0010](../adr/0010-a-check-is-not-a-check-until-it-has-failed-on-purpose.md).
