# Testing

The test tiers, what each needs and what only it can answer, the fixtures a test builds on, the
E2E legs, and the environment variables the suites read. The Make targets themselves are
[build-test-lint.md](build-test-lint.md); the CI jobs are [ci-and-release.md](ci-and-release.md).
Read against the tree on 2026-10-05.

**The only cluster this operator runs on is Kind.** The E2E tier runs on every pull request and
before every release ([ci-and-release.md](ci-and-release.md#the-e2e-jobs)) and was first observed
passing on 2026-10-05, locally with `make e2e-local` on both legs; nothing has been observed on a
production cluster.

## The tiers

Five tiers, separated by build tag. A tier answers what no cheaper tier can, and a test belongs in
the cheapest tier that can actually answer its question — never in one that cannot.

| Tier | Command | Build tag | Needs | What only it can answer |
|---|---|---|---|---|
| Unit | `make test-unit` | none | nothing | Builder output and reconcile logic, against controller-runtime's fake client. No control plane is started anywhere in this tier. |
| Integration | `make test-integration` | `integration` | the envtest binaries (`make envtest` and the `ENVTEST_K8S_VERSION` assets, both fetched by the target) | What a **real API server** decides: CRD defaulting and validation, whether the built objects are accepted, whether the owner references are accepted as written, whether the `Owns` watch carries a StatefulSet's readiness into status, and whether PodSecurity admission at `restricted` admits the broker pod. envtest runs no kubelet and no kube-controller-manager, so **no pod starts and no garbage collection happens here**. |
| E2E | `make test-e2e` against a cluster, or `make e2e-local` | `e2e` | a cluster with the operator installed from the chart, a kubeconfig, `kubectl`; cert-manager and the issuer for the TLS test; three schedulable nodes for the hard spread | A running broker: that the listener speaks MQTT and not merely TCP, that a cert-manager-issued Secret serves MQTTS, that hard anti-affinity really spreads, that deleting the CR really collects the owned objects. |
| Image tools | `make test-image-tools` | `imagetools` | Docker, no cluster | Whether the pinned broker image still contains the binaries this repository executes inside it. |
| RBAC parity | `make verify-rbac-parity` | `rbacparity` | helm, kustomize, git, no cluster | Whether the Helm and the kustomize install path grant the same authority. |

Two more guards are not Go tests and have no build tag — `make verify-ci-references` and
`make test-release-tooling` ([package-map.md](package-map.md#hack)). Like the image-tools and
RBAC-parity tiers they run without a cluster, which is what lets them run on every pull request
([ADR 0010](../adr/0010-a-check-is-not-a-check-until-it-has-failed-on-purpose.md) D7).

**Nothing is skipped.** No target passes `-short` and no test checks `testing.Short()`. The one
`t.Skip` in the tree is the hard-spread E2E test on a cluster with fewer than three schedulable
nodes, and `E2E_REQUIRE_MULTI_NODE=true` turns that into a failure.

Timeouts per tier: the integration target passes `-timeout=60m`, `test-e2e` `-timeout=30m`,
`test-image-tools` `-timeout=15m`; unit and RBAC parity use the Go default.

## Unit tests

Next to the code, in `api/v1`, `cmd`, `internal/builder`, `internal/common`,
`internal/controller` and `test/testimages`. testify throughout — `require` for preconditions,
`assert` for the claim.

| Fixture | Where | What it gives you |
|---|---|---|
| `newMosquitto(mutators…)`, `withTLS(secret)`, `withStorage(size)` | [`internal/builder/configmap_test.go`](../../internal/builder/configmap_test.go) | A minimal CR — `broker` in `messaging`, one replica — and the two mutators every builder test composes |
| `mustBuild(t, m)`, `containerVolumeMount(c, name)`, `podVolume(spec, name)` | [`internal/builder/statefulset_test.go`](../../internal/builder/statefulset_test.go) | A StatefulSet that must build, and lookups by name |
| `newCR(mutators…)`, `newReconcilerFor(t, objs…)`, `request()`, `reconciled(t, r, c)`, `readyCondition(t, m)` | [`internal/controller/mosquitto_controller_test.go`](../../internal/controller/mosquitto_controller_test.go) | A CR at generation 1; a reconciler over controller-runtime's fake client, seeded with objects and with the status subresource declared so status writes behave as on an API server; one pass and the stored resource; the `Ready` condition, required to exist |
| `testMosquitto()` | [`internal/common/labels_test.go`](../../internal/common/labels_test.go) | The CR the label tests use |
| `newTestFlagSet()` | [`cmd/main_test.go`](../../cmd/main_test.go) | A `FlagSet` that returns parse errors instead of exiting; the manager tests build against `http://127.0.0.1:1`, where nothing listens, and start nothing |

Tests that hold an invariant a reader might otherwise break:

| Test | Holds |
|---|---|
| `TestExtractVersionFromImage_AlwaysProducesAValidLabel`, `TestBaseLabels_AreAllValid` | every derived label value passes apimachinery's `validation.IsValidLabelValue` — asserted against the validator, not against expected strings |
| `TestSelectorLabelsSurviveAnImageChange` | the selector does not move with the image |
| `TestPodTemplateHashesChangeWithTheThingTheyDigest` | a change only the operator computes still changes the template |
| `TestReplicaChangeDoesNotRollThePods` | scaling rewrites a number; it does not touch the template |
| `TestStatefulSetHasChanged`, `TestStatefulSetHasChanged_NilReplicasIsNotDrift` | the drift decision ([architecture.md](architecture.md#what-each-write-compares)) |
| `TestBuildStatefulSet_SatisfiesRestrictedPodSecurityStandard` | the broker pod admits into a namespace enforcing the restricted Pod Security Standard, spelled out by hand — the integration test below asks an API server |
| `TestBuildStatefulSet_PodMetadataLosesToTheOperator`, `TestStatefulSetHasChanged_PodMetadata`, `TestMergeStatefulSet` | `spec.podLabels`/`spec.podAnnotations` reach the template under the operator's keys, roll the pods, and a removed key leaves; foreign keys and `kubectl.kubernetes.io/restartedAt` stay ([ADR 0009](../adr/0009-delete-only-through-owner-references.md) D9) |
| `TestBuildStatefulSet_ConfigCheckInitContainer` | the `config-check` init container runs the pod's own image with the broker's security context and never mounts the data volume ([broker-behaviour.md](broker-behaviour.md) M19) |
| `TestReconcile_UpdatesKeepForeignLabels`, `TestReconcile_ReplicaChangeKeepsTheTemplateMetadataOthersAdded`, `TestReconcile_PodLabelsReachAndLeaveTheTemplate` | the same merge through the reconciler, on all four objects |
| `TestReconcile_SecretSecurity`, `TestReconcile_SecretSecurityLeavesARunningBrokerAlone` | `--secret-security=true` refuses an unlabelled or missing TLS Secret before any write, with the reason, and polls nothing |
| `TestRender_IsDeterministic` | equal logins render equal bytes whatever the order, names and creation times of the users ([ADR 0013](../adr/0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md) D8); observed failing without the final sort |
| `TestRender_KeepsAHashWhileItsPasswordVerifies`, `TestReconcile_AnUnchangedPassWritesNothing` | an unchanged password keeps its hash and an unchanged pass writes nothing ([ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md) D3) |
| `TestRender_Refusals`, `TestRender_AcceptsWhatTheAllowlistAllows`, `TestRender_TheOldestUserKeepsTheUsername` | every render-time refusal, the username allowlist, and the collision rule |
| `TestVerifyPassword_AcceptsTheBrokersOwnHash`, `TestHashPassword_HasTheShapeMosquittoPasswdWrites` | the `$7$` format against a line `mosquitto_passwd` of the pinned image wrote (M20) |
| `TestReconcile_RendersTheUsersIntoTheAuthSecret`, `TestReconcile_ABrokerWithoutUsersAcceptsNobodyAndSaysSo`, `TestReconcile_EveryUserReason`, `TestReconcile_AUserOfAMissingBroker` | the users through the reconciler: `<name>-auth`, the statuses, every reason, and that a user's failure fails neither the broker nor another user |
| `TestReconcile_ARefusedConfigWritesNothing`, `TestValidateSpecConfig` | the `spec.config` allowlist; observed failing with `listener` allowed |
| `TestReconcile_ANamespaceOutsideTheGrant` | `--secret-namespaces` refuses a broker elsewhere and its users |
| `TestGenerateMosquittoConf_RequiresALogin` | both plugins, `listener_allow_anonymous false`, `use_username_as_clientid true`, one listener, in each shape |
| `TestBuildStatefulSet_TheCredentialsPath`, `TestBuildStatefulSet_TheReloaderImageRollsThePods` | `auth-init`, `reloader`, the two auth volumes and `shareProcessNamespace` in the pod; a new operator image rolls every broker |
| `TestStripSecret`, `TestManagerOptions_SecretCache` | the Secret cache holds no data, and only the granted namespaces |
| `TestBrokersForSecret`, `TestUserBrokers_AMoveWakesBothBrokers` | the Secret and user watches map to the right brokers |
| `TestSync_*`, `TestFindProcess`, `TestRun_SignalsOnChangeAndRetriesUntilTheBrokerIsThere`, `TestMain_Once` ([`internal/reloader`](../../internal/reloader/reloader_test.go)) | the copy through one resolved `..data`, mode `0600`, no temporary file left, a broken Secret changing nothing, one signal per change and a pending one retried; observed failing with the retry removed |
| `TestReconcile_RefusesForeignObjects`, `TestEnsureOwned` | an object this CR does not control is refused, not adopted, with the exact message of [ADR 0009](../adr/0009-delete-only-through-owner-references.md) D5 |
| `TestReconcile_DeletionIsLeftToGarbageCollection` | a CR with a `DeletionTimestamp` gets no writes |
| `TestReconcile_UnbuildableSpecFailsVisibly` | the one builder error surfaces as `Failed` |
| `TestReconcile_StatusPhases`, `TestObservedGenerationFollowsTheSpec`, `TestStatusUnchanged` | the status table and its no-op write |
| `TestMaxConcurrentReconciles`, `TestBindOperatorFlags_Defaults` | an operator without the flag, and a reconciler built without the field, get 4 workers |
| `TestBindOperatorFlags_AllFlagsParsed`, `TestZapFlagsAreBound` | the flag names the chart passes parse |
| `TestManagerOptions` | the Lease ID `mosquitto-operator.mko.gtrfc.com`, and `--metrics-bind-address=0` as "off" |
| `TestSetupWithManagerRegistersTheController` | the watch wiring registers |
| `TestDefault_*`, `TestMosquittoImageIsPinnedToATag` ([`test/testimages`](../../test/testimages/images_test.go)) | the image selector, and that the pin carries a `2.x` tag Renovate's regex can match |

## Integration tests

[`test/integration/`](../../test/integration), build tag `integration`. `TestMain` in
[`suite_test.go`](../../test/integration/suite_test.go) prepares one run for the whole package:

1. envtest starts an API server and etcd with the CRDs from `config/crd/bases`
   (`ErrorIfCRDPathMissing: true`, so a missing `make manifests` fails loudly instead of as "no
   matches for kind" in every test).
2. One controller manager, with the metrics listener off (`BindAddress: "0"`) and the real
   `MosquittoReconciler` registered — one for the package, because controller-runtime refuses two
   controllers of the same name in one process.
3. The manager runs in a goroutine; the suite waits for the cache to sync; `k8sClient` is the
   manager's client, so reads go through its cache.

Every test takes a namespace of its own; namespaces are never deleted, because envtest has no
namespace controller and a terminating namespace would never finish. The control plane is thrown
away at the end. The image in the specs is `fixtureImage = "example.test/mosquitto:integration"`:
never pulled, deliberately not the pin.

| Fixture | Where | What it gives you |
|---|---|---|
| `newNamespace(t)` | `suite_test.go` | `mko-int-<n>`, unique per test |
| `createMosquitto(t, ns, name, spec)` | `suite_test.go` | The CR as the API server stored it, so CRD defaulting is visible |
| `eventuallyGet(t, ns, name, out)`, `getMosquitto`, `waitForStatefulSet` | `suite_test.go` | Waits until an object exists — a read right after a create can lose the race with the cache |
| `waitForPhase(t, ns, name, phase)` | `suite_test.go` | Waits for a status phase |
| `isControlledBy(obj, name)` | `suite_test.go` | Whether an object carries a controller reference to the named `Mosquitto` |
| `eventuallyTimeout`, `eventuallyInterval` | `suite_test.go` | 30 s and 200 ms |

| File | What it proves |
|---|---|
| [`mosquitto_test.go`](../../test/integration/mosquitto_test.go) | One pass creates the four owned objects with controller references; the phase stays `Pending` because no kubelet ever makes a pod ready; a StatefulSet marked ready by the test (standing in for the StatefulSet controller) moves the CR to `Ready` through the `Owns` watch; a `spec.config` change reaches the ConfigMap and the config hash; a pre-existing foreign ConfigMap is refused (`Failed`, `ReconcileFailed`, kept unchanged, no StatefulSet); a storage spec renders a `data` claim template the API server accepts |
| [`crd_validation_test.go`](../../test/integration/crd_validation_test.go) | An empty spec defaults to one replica and `antiAffinity: off`; ten replicas, an unknown anti-affinity mode, an empty TLS secret name and an empty storage size are rejected |
| [`affinity_test.go`](../../test/integration/affinity_test.go) | `off` renders no affinity block, `soft` one preference, `hard` one requirement, each repelling only its own brokers |
| [`tls_test.go`](../../test/integration/tls_test.go) | The secret is mounted and the listener moves to 8883; the operator writes the StatefulSet whether or not the Secret exists, and never creates it |
| [`users_test.go`](../../test/integration/users_test.go) | A user applied before its broker and its Secret reports `BrokerNotFound`, then `SecretNotFound`, then becomes `Accepted` with nobody reconciling by hand — the watches carry each arrival; a deleted user leaves `<broker>-auth`. Observed failing with the Secret watch removed |
| [`pod_security_test.go`](../../test/integration/pod_security_test.go) | envtest's API server enforces PodSecurity — the control pod with `allowPrivilegeEscalation: true` is refused — and admits the pod of every shape the builder renders into a namespace labelled `enforce=restricted`, as a dry run ([ADR 0012](../adr/0012-the-first-release-is-one-broker-run-from-git-and-high-availability-is-parked.md) D4). A claim template becomes a PVC volume; the default ServiceAccount is created by the test because envtest runs no controller that would |

## E2E tests

[`test/e2e/`](../../test/e2e), build tag `e2e`, against a real cluster with the operator installed
from the chart. The package imports **nothing from `internal/`**: it addresses `mko.gtrfc.com/v1`,
`Mosquitto`, `mosquittoes`, the names, label keys and ports as literals, the way a user does — a
helper that imported the builder would agree with a renamed constant by construction. It does
import [`test/testimages`](../../test/testimages/images.go) for the broker image. cert-manager is
reached only through the dynamic client as unstructured objects, so it never enters the module
graph.

| Scenario | File | What only a cluster can show |
|---|---|---|
| `TestE2E_Mosquitto_ProvisionsAReachableBroker` | [`mosquitto_test.go`](../../test/e2e/mosquitto_test.go) | Status mirrors the StatefulSet with `Ready=True`; one port `mqtt`/1883; the generated config with `listener_allow_anonymous false`; both Services with endpoints; a retained publish with `mosquitto_pub` read back with `mosquitto_sub` inside the pod, as a `MosquittoUser` created before the broker so its first start carries it; deleting the CR collects the StatefulSet, both Services, the ConfigMap and `<name>-auth` (`waitForOwnedObjectsGone`, within 2 minutes) |
| `TestE2E_TLS_CertManagerIssuedSecretServesMQTTS` | [`tls_test.go`](../../test/e2e/tls_test.go) | A `Certificate` from the ClusterIssuer `e2e-ca-issuer` yields `tls.crt`, `tls.key`, `ca.crt`; the broker mounts it read-only at `/mosquitto/tls`, serves `mqtts`/8883 only, completes a publish/subscribe round trip verified against `ca.crt` by the pod's DNS name; the operator creates no `Certificate` of its own |
| `TestE2E_AntiAffinity_OffByDefault` | [`affinity_test.go`](../../test/e2e/affinity_test.go) | No affinity block without an opt-in |
| `TestE2E_AntiAffinity_SoftWhenRequested` | `affinity_test.go` | Three replicas become ready even where the spread cannot be satisfied; one preferred term at weight 100 |
| `TestE2E_AntiAffinity_HardSpreadsAcrossNodes` | `affinity_test.go` | Three replicas on three distinct nodes. Skips below three schedulable nodes (Ready, not cordoned, no `NoSchedule`/`NoExecute` taint) unless `E2E_REQUIRE_MULTI_NODE=true` |
| `TestE2E_Users_TheBrokerFollowsItsUsers` | [`users_test.go`](../../test/e2e/users_test.go) | On one running broker: an anonymous client is refused; a user created later can publish and nothing restarted; a client is refused outside its ACL; another user's client ID takes over no session; a changed password locks out the old one and admits the new one with no CR edited; a deleted user's open connection is dropped; a `spec.config` with a second listener is refused while the broker keeps serving; `<name>-auth` holds hashes only and is collected with the broker. Each change waits up to `reloadTimeout` (4 minutes) for the kubelet's refresh |
| `TestE2E_PodMetadata_ReachesAndLeavesThePods` | [`pod_metadata_test.go`](../../test/e2e/pod_metadata_test.go) | A label and an annotation of the CR are on the running pod; replacing them rolls the pod, the new key arrives and the removed one is gone |
| `TestE2E_ConfigCheck_StopsATypoBeforeTheBroker` | [`config_check_test.go`](../../test/e2e/config_check_test.go) | A misspelled `spec.config` directive fails the `config-check` init container, the broker container never runs, and the init container's log carries `Error: Unknown configuration variable '…'.` and `Error found at /mosquitto/config/mosquitto.conf:<line>.`, the line read back from the ConfigMap |

Why the reachability check exists: the readiness probe is a TCP connect, so a broker that accepts
connections and rejects every CONNECT still reports Ready. Only a real MQTT session says otherwise.

| Fixture | Where | What it gives you |
|---|---|---|
| `newTestClients(t)` | [`e2e_test.go`](../../test/e2e/e2e_test.go) | A typed and a dynamic client from `KUBECONFIG` or `~/.kube/config`, at 50 QPS / burst 100 so parallel polling tests do not starve the rate limiter |
| `tc.createNamespace(t, name)` | `e2e_test.go` | The namespace, recreated if a leftover exists, and its cleanup — which **keeps** the namespace when the test failed, so the failure collection still finds pods and events |
| `buildMosquittoObject`, `tc.createMosquitto`, `tc.deleteMosquitto`, `tc.waitForMosquittoDeleted` | `e2e_test.go` | The CR as an unstructured object, through the dynamic client |
| `tc.waitForMosquittoPhase`, `tc.getMosquittoStatus`, `tc.waitForStatefulSetReady`, `tc.waitForServiceEndpoints` | `e2e_test.go` | Polls every 2 s for up to 5 minutes |
| `tc.getStatefulSet`, `tc.getService`, `tc.getConfigMap`, `tc.listBrokerPods`, `assertLabelExists` | `e2e_test.go` | Reads and label checks |
| `tc.podExec(t, ns, pod, cmd…)` | `e2e_test.go` | `kubectl exec`, stdout only, 5 attempts with growing backoff, 30 s each |
| `tc.requireThreeSchedulableNodes(t)` | `affinity_test.go` | The node-count guard |
| `tc.createCertificate`, `tc.waitForCertificateReady`, `tc.waitForSecret`, `tc.getSecret` | `tls_test.go` | The cert-manager side, as an administrator would own it |
| `tc.updateMosquittoSpec(t, ns, name, fields)`, `tc.waitForBrokerPod(t, ns, name, what, accept)` | `pod_metadata_test.go` | A spec update that retries on the conflict the operator's status writes cause; a wait for a ready pod whose labels and annotations satisfy a predicate |
| `tc.createCredentials`, `tc.setPassword`, `tc.createUser`, `tc.waitForUserReady`, `acl(topic, access)` | `users_test.go` | A basic-auth Secret, a password change, a `MosquittoUser` through the dynamic client |
| `tc.mqtt`, `tc.publish`, `tc.receive`, `tc.eventuallyAccepted`, `tc.eventuallyRefused`, `tc.startSubscriber`, `tc.brokerPodIdentity` | `users_test.go` | One MQTT client run in the broker container, without retries — a refusal is an answer; polls until a login is accepted or refused (`not authorised`, exit 5); a long-running subscriber; the pod's UID and the broker's restart count, to prove nothing restarted |

**Never assert on `secret.Data`.** A testify failure prints the value it was given, the E2E log is
tee'd into the job output, and one red run would publish a private key. The TLS test asserts on the
key set instead.

### The two legs

The legs differ in node count, and only there
([ADR 0004](../adr/0004-two-e2e-legs-and-no-version-matrix.md)):

| Leg | Workers | Cluster | Filter | `E2E_REQUIRE_MULTI_NODE` |
|---|---|---|---|---|
| `single-node` | 0 | `mosquitto-operator-test` | none — the full suite | `false` |
| `multi-node` | 3 | `mosquitto-operator-test-multinode` | `TestE2E_AntiAffinity` | `true` |

Three workers, not two: Kind keeps the control-plane `NoSchedule` taint on multi-node clusters, so
spreading three replicas needs three schedulable workers. The CI job for the filtered leg greps
its own log for `--- PASS: TestE2E_AntiAffinity_HardSpreadsAcrossNodes`, so a filter that matched
nothing cannot pass as green. There is no version-line matrix; `E2E_MOSQUITTO_IMAGE` is the escape
hatch.

Reproduce a leg locally:

```bash
make e2e-local KIND_WORKERS=0                                   # single-node leg
E2E_REQUIRE_MULTI_NODE=true make e2e-local KIND_WORKERS=3 \
  KIND_CLUSTER=mosquitto-operator-test-multinode E2E_RUN=TestE2E_AntiAffinity   # multi-node leg
```

`e2e-local` builds the image with `docker build` directly (not `make docker-build`), installs the
chart with [`test/e2e/helm-values.yaml`](../../test/e2e/helm-values.yaml) — image
`mosquitto-operator:test` with `pullPolicy: Never`, leader election off — and does **not** preload
the broker image into the nodes, so the kubelet pulls `eclipse-mosquitto` anonymously. The CI
job does preload it, reading the tag out of `test/testimages/images.go`.

## Image-tools tests

[`test/imagetools/image_tools_test.go`](../../test/imagetools/image_tools_test.go), build tag
`imagetools`, Docker and no cluster.

- `TestImageProvidesEveryExecutedTool` runs `docker run --rm --entrypoint sh <pin> -c …` once and
  checks with `command -v` that the image provides the broker binary — **read from the builder's
  container command**, not written out again — and `clientTools` (`mosquitto_pub`,
  `mosquitto_sub`, which the E2E suite executes). stdout and stderr are kept apart because a cold
  pull writes its progress to stderr.
- `TestPinnedImageIsTheOperatorDefault` asserts `builder.DefaultImage == testimages.MosquittoImage`,
  because otherwise this tier would check an image no broker runs
  ([ADR 0007](../adr/0007-one-broker-image-pin-and-why-not-the-openssl-tag.md)).
- `TestImageProvidesTheFilePlugins` asks for `/usr/lib/mosquitto_password_file.so` and
  `/usr/lib/mosquitto_acl_file.so`, the paths the generated configuration loads.
- `TestImageAcceptsTheGeneratedConfiguration`
  ([`generated_config_test.go`](../../test/imagetools/generated_config_test.go)) runs the pinned
  image's `--test-config` over the generated file, plain and with TLS, with every allowed
  `spec.config` directive appended at a sample value — `allowlistSamples` must cover the allowlist
  exactly. Observed failing on a generated typo: `Error: Unknown configuration variable
  'use_username_as_client_id'.`
- `TestImageAcceptsTheHashTheOperatorRenders` and `TestOperatorVerifiesTheImagesHash`
  ([`password_hash_test.go`](../../test/imagetools/password_hash_test.go)) repeat
  [broker-behaviour.md](broker-behaviour.md) M20 on every pull request: inside one container run
  as `1883:1883`, a `$7$` line from `auth.HashPassword` loads into the `password-file` plugin and
  logs a client in with its password (`right=0`) and not with another (`wrong=5`); and a line the
  image's `mosquitto_passwd` writes verifies with `auth.VerifyPassword`. Observed failing with the
  key derived at 999 iterations under a `1000` label: `"right=5\nwrong=5" does not contain
  "right=0"`. `runInImageAs` is `runInImage` with `--user`.

A cold pull plus the probe is bounded by 5 minutes.

## RBAC-parity test

[`test/rbacparity/rbac_parity_test.go`](../../test/rbacparity/rbac_parity_test.go), build tag
`rbacparity`. It finds the repository root with `git rev-parse --show-toplevel` and renders each
entry of `installSettings` on both paths: `helm template parity deploy/helm/mosquitto-operator
--namespace mosquitto-operator-system` with the setting's `--set` values (none for the defaults,
so leader election on), and `kustomize build` of `config/default` — or, for a setting with
components, of an overlay of it the test writes to `tmp/rbacparity-<setting>/` (`bin/kustomize`,
else one on `PATH`). Two settings today: the defaults, and `secretSecurity` with
`config/components/secret-security`. It decodes every `ClusterRole` and `Role` into `rbacv1` types
and compares `(kind, apiGroup, resource) -> sorted verbs`, and it checks that the manager
container of both renders passes the same effective `--secret-security` (the last occurrence wins,
as in Go's flag package). Rule order, grouping and name prefixes wash out; a
namespaced `Role` and a `ClusterRole` with the same verbs are correctly *not* the same grant. An
empty parse on either side fails the test, so a decoder that reads nothing cannot pass. It logs
`compared N grants across both install paths`; the authority it compares is D6 of
[ADR 0006](../adr/0006-both-install-paths-grant-the-same-authority.md). It compares authority
and that one flag — `make install`'s un-overlaid rendering and the leader-election flag difference
between the two Deployments are checked by nothing.

## Environment variables the suites read

| Variable | Read by | Effect |
|---|---|---|
| `KUBEBUILDER_ASSETS` | integration (set by the Make targets) | where envtest finds its binaries |
| `KUBECONFIG` | e2e | the cluster; `~/.kube/config` when unset |
| `E2E_MOSQUITTO_IMAGE` | e2e, through `testimages.Default()` | the broker image the suite provisions; empty uses the pin |
| `E2E_REQUIRE_MULTI_NODE` | e2e, `requireThreeSchedulableNodes` | `true` turns the small-cluster skip into a failure |
| `E2E_RUN` | the `test-e2e` target | `-run` filter |
