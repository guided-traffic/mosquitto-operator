---
id: T2
title: tracked files state facts the code contradicts
state: dropped
severity: low
security: none
threat:
urgency: now          # rule 1: measured-false statements in tracked files
effort: S
blocked-by:
filed-from: the documentation restructuring of 2026-10-05
opened: 2026-10-05
decided:
done:
dropped-reason: folded into the project plan, which is the work list (ADR 0011 D12)
---

## Current state

Each statement below was checked against the tree on 2026-10-05; the code is right and the text
is wrong unless the open question says otherwise.

| Where | Says | The tree says |
|---|---|---|
| [ADR 0003](../../adr/0003-the-go-version-is-one-fact-in-four-files.md) `Status` | the four Go-version sites read `1.27.0` | `1.27.1` in [`go.mod:3`](../../../go.mod#L3), [`Containerfile:2`](../../../Containerfile#L2), [`release.yml:32`](../../../.github/workflows/release.yml#L32), [`build.yml:8`](../../../.github/workflows/build.yml#L8); the four agree with each other, only the record is stale |
| [`cmd/main.go:60-62`](../../../cmd/main.go#L60-L62) | "The deployment passes --zap-log-level" | neither [`deployment.yaml`](../../../deploy/helm/mosquitto-operator/templates/deployment.yaml) nor [`manager.yaml`](../../../config/manager/manager.yaml) passes it; `make run` does |
| [`internal/common/labels.go:99`](../../../internal/common/labels.go#L99) | cites `TestSanitizeLabelValue_AlwaysProducesAValidLabel` | the test is `TestExtractVersionFromImage_AlwaysProducesAValidLabel` ([`labels_test.go:125`](../../../internal/common/labels_test.go#L125)) |
| [`clusterrole.yaml:10-16`](../../../deploy/helm/mosquitto-operator/templates/clusterrole.yaml#L10-L16) | "nothing compares it against config/rbac/role.yaml"; "the leases rule below" | [`test/rbacparity`](../../../test/rbacparity/rbac_parity_test.go) compares them; there is no leases rule in that template — it is the namespaced Role of `leader-election.yaml` |
| [`values.yaml:47-49`](../../../deploy/helm/mosquitto-operator/values.yaml#L47-L49) | the leases rule is "in the ClusterRole" | it is in the namespaced Role ([ADR 0006](../../adr/0006-both-install-paths-grant-the-same-authority.md) D5) |
| [`Chart.yaml:3`](../../../deploy/helm/mosquitto-operator/Chart.yaml#L3), [`package.json:4`](../../../package.json#L4), [`Containerfile:44`](../../../Containerfile#L44), [`build.yml:71`](../../../.github/workflows/build.yml#L71) | "provisioning highly available Mosquitto MQTT brokers" | the brokers are independent processes, and high availability is parked ([ADR 0012](../../adr/0012-the-first-release-is-one-broker-run-from-git-and-high-availability-is-parked.md) D3) |
| [`internal/common/labels.go:47-48`](../../../internal/common/labels.go#L47-L48) | `spec.image` carries a `MinLength` | the CRD has none ([`mko.gtrfc.com_mosquittoes.yaml:100-104`](../../../config/crd/bases/mko.gtrfc.com_mosquittoes.yaml#L100-L104)) |
| [ADR 0005](../../adr/0005-fork-pull-requests-execute-on-the-self-hosted-runners.md) D5 | `renovate.yml` runs on push to `main` | it does not ([`renovate.yml:2-33`](../../../.github/workflows/renovate.yml#L2-L33)) |
| [ADR 0005](../../adr/0005-fork-pull-requests-execute-on-the-self-hosted-runners.md) D8, [`release.yml:1310-1311`](../../../.github/workflows/release.yml#L1310-L1311) | `npm ci --ignore-scripts` protects "while the token sits in the environment" | the app token is in the environment of the Release step only (`release.yml:1319-1322`), not during `npm ci` (`:1313-1314`); the protection is real, the stated reason is not |
| [`build.yml:102-105`](../../../.github/workflows/build.yml#L102-L105) | "no release has been built from this repository yet" | tags `v0.1.0` to `v0.1.8` exist; whether `build.yml` ran for them is not checkable from the tree |
| [ADR 0001](../../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md) Consequences | a missing Secret, a Secret without `tls.crt` and a mismatched key all end with the broker "in `CrashLoopBackOff`" | the README and [`test/integration/tls_test.go`](../../../test/integration/tls_test.go) describe a kubelet mount error for the missing Secret; Kubernetes holds a pod with a missing non-optional Secret volume in `ContainerCreating`, so the first case does not crash-loop — not observed here |

One statement was a disagreement between a comment and the code, now decided for the comment:
[`MapEntriesMissing`](../../../internal/common/labels.go#L174-L177) says labels other controllers and
users add are not reverted, and indeed ignores them when deciding whether to write — but every
write the operator makes assigns `current.Labels = desired.Labels`
([`mosquitto_controller.go:162,202,244`](../../../internal/controller/mosquitto_controller.go#L162)),
which drops them whenever an update happens for another reason.

## Required changes

1. Each row: the text corrected to what the tree says, in the file named. The Go-version
   correction in ADR 0003 is a `Status` fact, not a decision change. The "highly available"
   description becomes the README's pitch sentence, in all four places in one change.
2. The ADR 0001 sentence: observed on Kind with a `Mosquitto` naming a Secret that does not
   exist, then written as observed.
3. The label behaviour: [ADR 0009](../../adr/0009-delete-only-through-owner-references.md) D9 —
   the three label assignments, the pod-template labels and the pod-template annotations merge
   instead of replacing, the operator's own keys winning; a unit test with a foreign label on each
   of the three kinds and a foreign label and `kubectl.kubernetes.io/restartedAt` on the pod
   template, observed failing against today's assignments. The runtime page's paragraph on
   `kubectl rollout restart` rewritten in the same change.
