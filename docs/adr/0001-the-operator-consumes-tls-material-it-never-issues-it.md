# ADR 0001: The operator consumes TLS material, it never issues it

## Status

Accepted. Date: 2026-09-01.

**Verified by reading, in this repository:**
[`api/v1/mosquitto_types.go`](../../api/v1/mosquitto_types.go) (`MosquittoTLS`, `SecretName`,
`IsTLSEnabled`), [`internal/builder/statefulset.go`](../../internal/builder/statefulset.go)
(`TLSVolumeName`, the `corev1.SecretVolumeSource`, the read-only mount, `AnnotationPodSpecHash`,
`AnnotationConfigHash`, `hashOf`, `StatefulSetHasChanged`),
[`internal/builder/configmap.go`](../../internal/builder/configmap.go) (`GenerateMosquittoConf`,
`TLSMountPath`, `TLSCertKey`, `TLSKeyKey`),
[`internal/builder/service.go`](../../internal/builder/service.go) (`brokerServicePort`),
[`internal/controller/mosquitto_controller.go`](../../internal/controller/mosquitto_controller.go)
(`SetupWithManager` and the `+kubebuilder:rbac` markers),
[`config/rbac/role.yaml`](../../config/rbac/role.yaml),
[`deploy/helm/mosquitto-operator/templates/clusterrole.yaml`](../../deploy/helm/mosquitto-operator/templates/clusterrole.yaml),
[`deploy/helm/mosquitto-operator/values.yaml`](../../deploy/helm/mosquitto-operator/values.yaml),
[`deploy/helm/mosquitto-operator/Chart.yaml`](../../deploy/helm/mosquitto-operator/Chart.yaml),
[`go.mod`](../../go.mod), [`Makefile`](../../Makefile) (`cert-manager-install`),
[`.github/workflows/release.yml`](../../.github/workflows/release.yml) (the `Install cert-manager`
step), [`test/e2e/tls_test.go`](../../test/e2e/tls_test.go),
[`test/e2e/testdata/cert-manager-issuer.yaml`](../../test/e2e/testdata/cert-manager-issuer.yaml),
[`test/integration/tls_test.go`](../../test/integration/tls_test.go),
[`internal/builder/statefulset_test.go`](../../internal/builder/statefulset_test.go).

**Implemented:** D1 through D6 — the Secret reference, the mount, the MQTTS listener, the port
move on both Services, the absent Secret watch and the absent Secret RBAC are all present in the
tree as described.

**Open:** D7's rotation trigger is deliberately not built. Nothing rolls a broker pod when the
referenced Secret changes.

**Not verified:** nothing in this repository has ever been observed running against a real
cluster. The E2E suite
([`test/e2e/tls_test.go`](../../test/e2e/tls_test.go)) encodes the intended behaviour, but no run
of it is evidence available here. Separately, **whether the `mosquitto` process itself would pick
up a replaced `certfile`/`keyfile` on some signal is not verified** — see Residual risks.

## Context

A broker that speaks MQTTS needs a certificate and a private key. There are two ways an operator
can get them there: issue them, or consume them. Issuing means owning a certificate authority or
a cert-manager `Certificate`, and with it renewal, revocation, key storage and the RBAC to read
private keys cluster-wide. Consuming means the material is somebody else's object and the operator
only mounts it.

Three concrete forces pushed this project to consuming:

* **The chart must install on a cluster that does not run cert-manager.** A chart dependency, or a
  Go import of the cert-manager API types, makes cert-manager a precondition for installing an
  operator whose default resource does not use TLS at all.
* **Both ways of producing the material are legitimate.** A hand-made
  `kubectl create secret tls` and a cert-manager-issued Secret produce the same two keys,
  `tls.crt` and `tls.key`. An API that accepts only one of them rejects half its users for no
  technical reason.
* **Reading a private key is a privilege, and this operator does not otherwise need it.** The
  ClusterRole it ships grants `configmaps`, `services`, `statefulsets` and the `mosquittoes` CRD —
  and nothing on `secrets`. The kubelet mounts the Secret into the pod; the operator never sees
  its bytes.

The cost of consuming is one specific gap, and this ADR exists mostly to write that gap down
rather than to celebrate the decision: an operator that does not read the Secret also cannot
notice when it changes.

## Decision

**D1 — `spec.tls.secretName` is the only path TLS material takes into a broker, and it names an
object this operator did not create.**
`MosquittoTLS` has exactly one field, `SecretName`, with
`+kubebuilder:validation:MinLength=1`. The rendered CRD spec has seven properties —
`antiAffinity`, `config`, `image`, `replicas`, `resources`, `storage`, `tls` — and none of them
describes an issuer, a duration, a key algorithm or a set of DNS names. There is nothing in the
API for the operator to issue *from*.

**D2 — Both ways of filling that Secret are first class, and neither of them is this project.**
The doc comment on `MosquittoTLS.SecretName` names them as 1. and 2.: `kubectl create secret tls`,
or a cert-manager `Certificate` the administrator owns. The same two are named in
[`values.yaml`](../../deploy/helm/mosquitto-operator/values.yaml) and in
[`README.md`](../../README.md). The operator's behaviour is identical in both cases, because it
only ever reads the name.

**D3 — Nothing this project ships depends on cert-manager, at any layer.**
[`Chart.yaml`](../../deploy/helm/mosquitto-operator/Chart.yaml) declares no `dependencies` block.
[`go.mod`](../../go.mod) requires no cert-manager module. The only cert-manager in the repository
is test infrastructure: `make cert-manager-install` applies the upstream
`v1.17.2` manifest and then
[`test/e2e/testdata/cert-manager-issuer.yaml`](../../test/e2e/testdata/cert-manager-issuer.yaml),
and the `Install cert-manager` step of
[`.github/workflows/release.yml`](../../.github/workflows/release.yml) does the same with a
five-attempt retry around the issuer apply. `TestE2E_TLS_CertManagerIssuedSecretServesMQTTS`
writes its `Certificate` through the **dynamic client** as an
`unstructured.Unstructured` against `certificateGVR`, which is what keeps the cert-manager types
out of the module graph even in the test tier.

**D4 — Enabling TLS moves the single listener; it does not add one.**
`IsTLSEnabled()` is `Spec.TLS != nil && Spec.TLS.SecretName != ""` — a half-filled spec counts as
disabled, so no listener is ever generated with no certificate to serve. Under TLS,
`GenerateMosquittoConf` emits `listener 8883`, `certfile /mosquitto/tls/tls.crt` and
`keyfile /mosquitto/tls/tls.key` instead of the `listener 1883` block; `BrokerPort` and
`BrokerPortName` return `8883`/`mqtts`, and `brokerServicePort` feeds both the headless and the
ClusterIP Service from those same two functions, so the port never has to be kept in sync twice.
The plaintext listener is gone rather than left open — `test/e2e/tls_test.go` asserts
`assert.NotContains(t, conf, "listener 1883")` and `require.Len(t, container.Ports, 1)`.

**D5 — The Secret is mounted whole and read-only, and the generated config names exactly two of
its keys.**
`buildPodSpec` adds a `corev1.SecretVolumeSource` with `SecretName: m.Spec.TLS.SecretName`,
`DefaultMode` `0o644`, and **no `Items` projection**; `buildBrokerContainer` mounts it at
`TLSMountPath = "/mosquitto/tls"` with `ReadOnly: true`. Whatever else the Secret carries appears
in that directory — this is how the E2E client reaches
`caCertPath = "/mosquitto/tls/ca.crt"` from a cert-manager CA-issued Secret without the operator
knowing that key exists. The generated configuration references only `TLSCertKey = "tls.crt"` and
`TLSKeyKey = "tls.key"`.

**D6 — The operator does not watch the referenced Secret, and holds no permission to read one.**
`SetupWithManager` registers `For(&mkov1.Mosquitto{})` plus `Owns` on `appsv1.StatefulSet`,
`corev1.ConfigMap` and `corev1.Service`. There is no `Owns(&corev1.Secret{})` and no `Watches`
call at all. The `+kubebuilder:rbac` markers grant `configmaps`, `services`, `statefulsets`,
`mosquittoes`, `mosquittoes/status` and `mosquittoes/finalizers`; the generated
[`config/rbac/role.yaml`](../../config/rbac/role.yaml) and the chart's
[`clusterrole.yaml`](../../deploy/helm/mosquitto-operator/templates/clusterrole.yaml) contain no
`secrets` rule, and the chart says so in a comment rather than by omission. **Not watching is not
an oversight; it is the privilege boundary.**

**D7 — A rotated certificate reaches a running broker only when its pod restarts, and this is
stated in the API rather than worked around.**
The pod template carries two annotations, `mko.gtrfc.com/pod-spec-hash` (a digest of the pod spec)
and `mko.gtrfc.com/config-hash` (a digest of the generated `mosquitto.conf`), and
`StatefulSetHasChanged` compares exactly those two plus the replica count and the labels. The pod
spec contains the Secret **name**, so pointing `spec.tls.secretName` at a *different* Secret
changes the pod-spec hash and rolls the StatefulSet — derived by reading `buildPodSpec` and
`hashOf`, not pinned by a test that renames a Secret. Changing the bytes **inside** the referenced
Secret changes neither hash, because neither digest has ever seen those bytes. The doc comment on
`SecretName`, the chart values and the README all say the same sentence: a rotation takes effect
once the pods restart, for example
`kubectl rollout restart statefulset/<name>`.

**D8 — What would close D7's gap is named, and deliberately not built.**
The closing move is a third annotation: a digest of the *content* of the referenced Secret,
computed by the operator and written into the pod template, so that a rotation changes the pod
template and the StatefulSet controller rolls the pods on its own. It is not built because it is
not free, and the price is exactly the privilege D6 spends:

* the operator would need `get`, `list` and `watch` on `secrets` — today it has none, cluster-wide
  or otherwise;
* it would need a `Watches` on `corev1.Secret` mapping back to the referencing `Mosquitto`, since
  `Owns` does not cover an object the operator did not create;
* a digest of certificate material is safe to publish (a certificate is public and a private key
  is not brute-forceable from a digest), so the annotation itself is not the objection — the read
  permission is;
* and the roll it triggers is a full restart of every broker pod, which for this workload means
  every connected MQTT client reconnects, because the pods share no session state.

Until that trade is made deliberately, the restart is the user's to run.

**D9 — TLS here encrypts and authenticates the broker. It authenticates no client.**
`GenerateMosquittoConf` emits no `require_certificate` — the string does not occur in the
generator at all — and it appends `allow_anonymous true` **outside** the `IsTLSEnabled()` branch,
so it is present in both the plaintext and the MQTTS configuration. A TLS-enabled broker is
therefore an *encrypted anonymous* broker: any client that can route to the ClusterIP Service and
complete a TLS handshake can publish and subscribe. This is stated in the type documentation of
`MosquittoSpec`, on `MosquittoTLS`, in the generator's own comment and in the README. **It is a
confidentiality feature, not an access-control feature, and it must not be sold as one.**

## Consequences

* **A TLS-enabled `Mosquitto` is not an authenticated one.** Turning TLS on changes who can read
  the traffic, not who can publish to it. Anyone reaching for `spec.tls` to lock a broker down has
  reached for the wrong field, and today the API offers no right one — authentication has to go
  through `spec.config`, which nothing validates.
* **A certificate renewal silently does nothing until somebody restarts the pods.** cert-manager
  renews on its own schedule; the Secret changes; the file in `/mosquitto/tls` is expected to
  follow it, because the mount uses no `subPath` — that half is standard Kubernetes Secret-volume
  behaviour and was **not** verified against a cluster here, only the absence of `subPath` was;
  and the running broker keeps serving what it parsed at start. There is no event, no condition
  and no log line anywhere in this operator that marks the moment. **This is the sharpest edge in
  the whole TLS story.**
* **An expiring certificate therefore becomes an outage on a timer**, on exactly the clusters that
  automated issuance was supposed to protect — a long-lived pod outlives its own certificate.
* **The operator cannot validate the Secret at all.** A `secretName` pointing at a missing Secret,
  a Secret with no `tls.crt`, or a `tls.key` that does not match the certificate produces a
  StatefulSet that reconciles cleanly and pods that fail to start. The reconcile is green; the
  broker is in `CrashLoopBackOff`. `test/integration/tls_test.go` creates a Secret whose values are
  the literal strings `not-a-certificate` and `not-a-key`, and the operator is perfectly happy —
  envtest runs no kubelet, so nothing ever tries to parse them.
* **The privilege footprint stays small, and that is the payment for the two points above.** The
  operator holds no `secrets` verb, so a compromised operator leaks no private key that it was not
  already leaking through the pods it schedules.
* **The E2E suite needs cert-manager on the cluster even though the product does not.** That
  install lives in `make cert-manager-install` and in the workflow, and the version `v1.17.2` is
  written out in both places — two literals that can drift apart.
* **`spec.config` can undo any of this.** It is appended after everything generated, so a repeated
  global option wins and an added `listener` line creates a listener the operator neither models
  nor exposes as a container or Service port. A `spec.config` that adds a plaintext listener next
  to the MQTTS one is accepted without comment.

## Alternatives Considered

### The operator creates and owns a cert-manager `Certificate`

Rejected. It makes cert-manager a hard precondition for a feature half the users would fill by
hand, and it puts the operator into the renewal business without giving it the one thing renewal
needs here — a way to get a running broker to re-read its files. It would also have to be written
against `unstructured` to stay installable without cert-manager, which is real complexity in
exchange for a `Certificate` the administrator can write in nine lines of YAML.

### A typed cert-manager Go dependency

Rejected for the same reason, harder: a compile-time import of the cert-manager API is a
dependency of the *operator binary*, not just of the feature, and `go.mod` is the one place where
"optional" cannot be expressed.

### Ship cert-manager as a chart dependency

Rejected. Installing a cluster-wide certificate authority as a side effect of installing an MQTT
broker operator is not a decision this chart gets to make for an administrator.

### A self-signed certificate generated by the operator when `spec.tls` is set without a Secret

Rejected. It produces material no client trusts, so every client needs the operator's CA
distributed to it anyway — at which point the administrator is doing the work the operator claimed
to save, plus debugging why the handshake fails.

### Hash the Secret content now and roll on rotation (D8, deliberately not built)

Not taken for this version. It is the right answer eventually, and it is written down in D8 so the
next person does not have to rediscover it — but it buys `secrets` read access cluster-wide, and
that trade deserves its own decision rather than arriving as an implementation detail of a TLS
feature.

### Send the broker a signal instead of restarting the pod

Not taken, and it is not currently possible from here: the operator holds no `pods` permission at
all, let alone `pods/exec`, the broker container runs with `readOnlyRootFilesystem: true` and
`AutomountServiceAccountToken` set to `false`, and no `lifecycle` hook or sidecar exists to deliver
a signal. Whether a signal would even work is the unverified half — see Residual risks.

### Require client certificates (`require_certificate true`)

Not taken. It would make every client of every broker hold issued material this operator does not
manage, and the API has no field to configure it. Recorded here so that D9's honesty about what
TLS buys is read as a stated limit and not as an oversight.

## Residual risks

* **The rotation gap is open and accepted (D7).** Nothing in this repository notices a changed
  Secret. The mitigation is documentation in three places and a manual
  `kubectl rollout restart`.
* **Whether the `mosquitto` process could re-read rotated material on a signal is not verified.**
  It does not matter for the current behaviour — the operator sends nothing and can send nothing
  (no `pods` RBAC, no lifecycle hook) — but it decides whether a future fix is "roll the pods" or
  "signal the pods", and this ADR does not answer it. It was not tested here.
* **No behaviour in this ADR has been observed on a real cluster.** The unit and integration tiers
  never start a container: envtest runs an API server and etcd and no kubelet. Everything about
  what the *broker* does with the mounted files is asserted only by `test/e2e/tls_test.go`, and no
  run of that suite is evidence available in this repository.
* **Anonymous access under TLS is accepted, not mitigated (D9).** The only barrier is the network,
  and this project ships no NetworkPolicy.
* **A wrong `spec.tls.secretName` is not reported anywhere.** There is no validation, no condition
  and no event; the symptom is a broker pod that will not start.
* **The cert-manager version `v1.17.2` is duplicated** between the `cert-manager-install` target
  and the workflow step, and nothing checks that the two agree.

## References

* [`api/v1/mosquitto_types.go`](../../api/v1/mosquitto_types.go) — `MosquittoTLS`, `SecretName`,
  `IsTLSEnabled`, and the anonymous-access note on `MosquittoSpec`
* [`internal/builder/statefulset.go`](../../internal/builder/statefulset.go) — `TLSVolumeName`,
  the Secret volume, the read-only mount, `AnnotationPodSpecHash`, `AnnotationConfigHash`,
  `StatefulSetHasChanged`
* [`internal/builder/configmap.go`](../../internal/builder/configmap.go) —
  `GenerateMosquittoConf`, `TLSMountPath`, `TLSCertKey`, `TLSKeyKey`, `allow_anonymous true`
* [`internal/builder/service.go`](../../internal/builder/service.go) — `brokerServicePort`, the
  single port both Services expose
* [`internal/controller/mosquitto_controller.go`](../../internal/controller/mosquitto_controller.go)
  — `SetupWithManager` and the `+kubebuilder:rbac` markers
* [`config/rbac/role.yaml`](../../config/rbac/role.yaml),
  [`deploy/helm/mosquitto-operator/templates/clusterrole.yaml`](../../deploy/helm/mosquitto-operator/templates/clusterrole.yaml)
  — the absent `secrets` rule
* [`deploy/helm/mosquitto-operator/values.yaml`](../../deploy/helm/mosquitto-operator/values.yaml),
  [`deploy/helm/mosquitto-operator/Chart.yaml`](../../deploy/helm/mosquitto-operator/Chart.yaml) —
  the TLS comment block, and the absent dependency
* [`go.mod`](../../go.mod) — no cert-manager module
* [`Makefile`](../../Makefile) — `cert-manager-install`
* [`.github/workflows/release.yml`](../../.github/workflows/release.yml) — the
  `Install cert-manager` step
* [`test/e2e/tls_test.go`](../../test/e2e/tls_test.go) —
  `TestE2E_TLS_CertManagerIssuedSecretServesMQTTS`, `certificateGVR`, `caCertPath`
* [`test/e2e/testdata/cert-manager-issuer.yaml`](../../test/e2e/testdata/cert-manager-issuer.yaml)
  — the `e2e-ca-issuer` `ClusterIssuer`
* [`test/integration/tls_test.go`](../../test/integration/tls_test.go) —
  `TestIntegration_TLS_MountsTheSecretAndMovesTheListener`
* [`internal/builder/statefulset_test.go`](../../internal/builder/statefulset_test.go) —
  `TestPodTemplateHashesChangeWithTheThingTheyDigest`
* [ADR 0007](0007-one-broker-image-pin-and-why-not-the-openssl-tag.md) — why the pinned broker
  image can serve TLS at all, and why the `-openssl` tag is not used
* [README.md](../../README.md) — the user-facing TLS section
