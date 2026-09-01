# ADR 0002: The Broker Metrics Exporter Is Written in This Repository

## Status

Accepted. Date: 2026-09-01. **Nothing in this ADR is implemented.** It is recorded ahead of the
code so that nothing else is built against a contradicting assumption.

What was verified by reading the tree, on 2026-09-01:

* **No exporter exists.** `cmd/` contains exactly `main.go` and `main_test.go`. There is no
  `cmd/exporter`.
* **`spec.metrics` is not in the API.** [`api/v1/mosquitto_types.go`](../../api/v1/mosquitto_types.go)
  declares `MosquittoSpec` with `Replicas`, `Image`, `Config`, `AntiAffinity`, `TLS`, `Resources`
  and `Storage` — and nothing else. A case-insensitive grep for `metrics` over
  [`config/crd/bases/mko.gtrfc.com_mosquittoes.yaml`](../../config/crd/bases/mko.gtrfc.com_mosquittoes.yaml)
  returns no match.
* **No sidecar is built.** `buildPodSpec` in
  [`internal/builder/statefulset.go`](../../internal/builder/statefulset.go) sets
  `Containers: []corev1.Container{buildBrokerContainer(m)}` — one container, unconditionally.
  Tests assert the single container from the other side, among them
  (`TestE2E_Mosquitto_ProvisionsAReachableBroker`, `TestIntegration_TLS_MountsTheSecretAndMovesTheListener`,
  `TestE2E_TLS_CertManagerIssuedSecretServesMQTTS`).
* **No MQTT client is a dependency.** [`go.mod`](../../go.mod) contains no `paho` line.
  `github.com/prometheus/client_golang v1.23.2` appears there marked `// indirect`.
* **The operator registers no metric of its own.** A grep for `prometheus.` and
  `metrics.Registry` over `api/`, `cmd/` and `internal/` returns nothing.
* The operator's own endpoint, described in D8 below, is implemented: see
  [`cmd/main.go`](../../cmd/main.go),
  [`deploy/helm/mosquitto-operator/templates/deployment.yaml`](../../deploy/helm/mosquitto-operator/templates/deployment.yaml)
  and [`deploy/helm/mosquitto-operator/templates/service.yaml`](../../deploy/helm/mosquitto-operator/templates/service.yaml).

What was **measured**, and how, so the numbers below are checkable rather than quoted: every `$SYS`
figure in the Context comes from running `eclipse-mosquitto:2.1.2-alpine` — the tag
[`internal/builder/statefulset.go`](../../internal/builder/statefulset.go) pins as `DefaultImage`
— and, for the cross-version comparison, `eclipse-mosquitto:2.0.22`, on a workstation with
`docker run --entrypoint sh`, a three-line configuration (`log_dest stdout`, `listener 1883`,
`allow_anonymous true`), and `mosquitto_sub -v -t '$SYS/#' -W 26` from the same image, reduced to
distinct topic names. Method and results are in the Context.

What was **not** verified: nothing in this repository has ever run against a real cluster, so no
scrape, no sidecar and no Service of the shape decided here has been observed working. The survey
of third-party exporters in the Context is a point-in-time measurement taken outside this
repository on 2026-09-01 and cannot be re-derived from this tree. What controller-runtime
registers on the operator's own endpoint by default was not enumerated; only the absence of any
registration of ours was checked.

## Context

Two metrics surfaces are in scope, and they are easy to confuse:

* the **broker** statistics of each provisioned Mosquitto, which is what a user monitoring an MQTT
  deployment wants;
* the **operator's own** controller-runtime endpoint, which is already there.

### Mosquitto exposes no Prometheus endpoint

Broker statistics exist only as MQTT messages on the `$SYS/#` topic tree. Anything that wants them
in Prometheus has to be an MQTT client first.

Measured against the pinned image `eclipse-mosquitto:2.1.2-alpine`, an idle broker with a single
subscriber published **55 distinct `$SYS/broker/...` topics** in a 26-second window — more than the
"roughly 40 documented counters and gauges" the decision was originally taken on, which is worth
knowing before anyone budgets the mapping work. The set is structural, not workload-dependent:
`bytes/{received,sent}`, `clients/{active,connected,disconnected,expired,inactive,total}`,
`heap/{current,maximum}`, `messages/{received,sent,stored}`, `publish/...`, `store/...`,
`subscriptions/count`, `shared_subscriptions/count`, `retained`, `connections/socket/count`,
`packet/out/{bytes,count}`, `uptime`, `version`, and the 24 `load/.../{1min,5min,15min}` series.

Three properties of that set matter for the decision:

* **It is small.** Fifty-five topics, all of them named by the broker itself.
* **It is nearly stable, but not frozen — and this was measured, not assumed.** The same probe
  against `eclipse-mosquitto:2.0.22` published **51** topics. Going from 2.0.22 to 2.1.2-alpine
  **adds five** (`connections/socket/count`, `heap/current`, `heap/maximum`, `packet/out/bytes`,
  `packet/out/count`) and **removes one** (`clients/maximum`). So a topic-to-metric mapping is
  version-sensitive at the edges: about a 10 % change across one minor version, on a base that is
  otherwise identical. Small enough to own; not so small that "write it once" is honest.
* **Not all of it is numeric.** `$SYS/broker/version` carries a string, and `$SYS/broker/uptime`
  carries `"<n> seconds"` rather than a number. A mapping layer is required; a generic
  "topic value becomes a gauge" tool is not enough.

The publish cadence was measured too: three consecutive `$SYS/broker/uptime` messages arrived at
0 s (the retained value, delivered on subscribe), 2 s and 12 s, so the **default interval on this
image is 10 seconds**. `GenerateMosquittoConf` in
[`internal/builder/configmap.go`](../../internal/builder/configmap.go) emits **no `sys_interval`
line at all**, so every broker this operator provisions inherits that default silently — and
Mosquitto 2.1 aligns `$SYS` updates to `sys_interval`, which makes the interval the scrape
resolution, not a detail.

### No maintained third-party exporter models broker statistics

Surveyed on 2026-09-01, outside this repository:

| Project | State |
|---|---|
| `sapcc/mosquitto-exporter` | Last code commit 2021-10-25, release v0.8.0 the same day. 22 open issues, 149 stars, not archived — five years stale. |
| `jnovack/mosquitto-exporter` | Archived. |
| `pfinal` (PHP, 2021), `uhlig-it` (Go, 2024), `Alessandrovito` (Java, 2018) | Single-star personal projects. |
| `hikhvar/mqtt2prometheus` | 428 stars, active — but it converts **application** payloads to metrics. It does not model broker statistics. |
| `kpetremann/mqtt-exporter` | 165 stars, active — same shape, same gap. |
| `eclipse-paho/paho.mqtt.golang` | Active: 3120 stars, pushed 2026-08-24. |

So the choice is not "depend or write", it is "adopt an unmaintained project or write". The one
piece of the problem that is genuinely hard — a correct, reconnecting MQTT client — is exactly the
piece that *is* actively maintained.

### Why "adopt the maintained exporter" is the usual answer, and why it loses here

D1 goes against the standard instinct for this problem — and against the choice made by the sibling
operator whose scaffolding and ADR discipline this project was modelled on, which is a maintainer
statement about another repository and not something this tree records. That instinct is: **take a
maintained third-party exporter, run it as a sidecar, depend on it.** It was the right call there
and it is the wrong one here, and the rule behind both is the same — only the inputs differ.

|  | the usual case | here |
|---|---|---|
| Surface to cover | Large and moving — hundreds of statistics fields, changing across datastore versions | 55 `$SYS` topics, moving by about six per minor version |
| Maintained equivalent | Yes, widely deployed | None |
| Cost of depending | Low: somebody else tracks the surface for you | High: adopt a five-year-stale project, or fork it |
| Cost of owning | High: track a large surface forever | Bounded: a topic-to-metric table and a client somebody else maintains |

Depending is cheaper when the surface is large and somebody else is already tracking it. Owning is
cheaper when the surface is small, slow-moving, and nobody is tracking it at all. **The answer
inverts because the inputs invert, not because the taste changed** — and if a maintained `$SYS`
exporter appears, the inputs change back and so should D1.

### The operator's own endpoint already exists

It is unrelated to the broker exporter and is documented here (D8) so that nobody conflates the
two while implementing.

## Decision

**D1 — The broker exporter is written in this repository, as `cmd/exporter`.** No third-party
exporter is adopted. The scope is a `$SYS/#` subscription and a mapping to a Prometheus registry;
the MQTT client itself is `github.com/eclipse-paho/paho.mqtt.golang`, which is the maintained part
of the problem and the only new direct dependency the feature adds. (That module path is recorded
from the decision, **not** verified: nothing in [`go.mod`](../../go.mod) imports it yet, so
whichever path `go get` resolves is what the code will carry.)

**D2 — `cmd/exporter` is a second binary in the *same* image, never a second image.**
[`Containerfile`](../../Containerfile) today builds `./cmd/main.go` to `-o manager`, copies that
one file into `gcr.io/distroless/static-debian12:nonroot` and sets
`ENTRYPOINT ["./manager"]`. The exporter adds a second `go build` and a second `COPY`, and the
broker pod's sidecar runs the operator image with a different entrypoint. One image means one tag,
one signature, one Renovate pin and one malware scan — the release pipeline in
[`.github/workflows/release.yml`](../../.github/workflows/release.yml) gains no job.

**D3 — It runs as a sidecar on each broker pod and connects over `localhost`.** Per-pod, because
`$SYS` is broker-local: each pod of a `Mosquitto` is an independent broker process
(the pitch paragraph of [`README.md`](../../README.md), and the `replicas` row of its spec reference), so a central scraper would have to reach
every pod individually anyway. Over `localhost`, because that connection never leaves the pod's
network namespace: no data port is exposed anywhere new, and no MQTT credential travels over the
cluster network.

**D4 — The exporter gets no credential and no API access of its own.** It reuses what the operator
already mounts into the pod: under TLS, the volume `TLSVolumeName` (`"tls"`) at `TLSMountPath`
(`/mosquitto/tls`), carrying `TLSCertKey` and `TLSKeyKey` — and, once this API models
authentication ([ADR 0008](0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)),
whatever principal that brings. It stays inside the pod's existing posture:
`AutomountServiceAccountToken: ptr.To(false)` is kept, and the ClusterRole gains **no** rule —
[`config/rbac/role.yaml`](../../config/rbac/role.yaml) grants nothing on `secrets` today and must
not start.

**D5 — The exporter carries no readiness probe.** A readiness probe on a sidecar makes the whole
pod unready when that sidecar fails, which removes the pod from both Services built by
[`internal/builder/service.go`](../../internal/builder/service.go). **A monitoring failure must not
take a broker out of its Service.** The price is stated in Consequences rather than hidden.

**D6 — The feature is gated on a CR field, and no broker pod grows a container it did not ask
for.** The working name is `spec.metrics`; **this ADR deliberately fixes neither the field's shape
nor its default**, because neither is written and inventing them here would create a second
authority for the code to contradict. What is fixed is the gate: absent the field, the pod spec is
byte-for-byte what it is today.

**D7 — When the exporter lands, the generated configuration states `sys_interval` explicitly.**
`GenerateMosquittoConf` emits no such line today, so the scrape resolution is an inherited default
(measured at 10 s on the pinned image) that no manifest shows. An exporter whose resolution is
invisible in the spec is a monitoring surface nobody can reason about. The same rule covers every
broker default that moved between versions and that the generated block currently inherits in
silence — `max_packet_size` is the other known candidate, 2.1 having lowered it from the MQTT
maximum to 2,000,000 bytes (an upstream release note, **not** verified here; no `max_packet_size`
line exists anywhere in this tree today).

**D8 — The operator's own metrics endpoint is a separate surface, and it stays unauthenticated
plain HTTP.** `bindOperatorFlags` ([`cmd/main.go`](../../cmd/main.go)) declares
`--metrics-bind-address` defaulting to `:8080` and `--health-probe-bind-address` defaulting to
`:8081`; `managerOptions` applies the first as
`Metrics: metricsserver.Options{BindAddress: f.metricsAddr}` and the second as
`HealthProbeBindAddress`. The chart's `deployment.yaml` passes
`--metrics-bind-address={{ if .Values.metrics.enabled }}:8080{{ else }}0{{ end }}`, where `0` is
the literal controller-runtime reads as "do not start the metrics server", and `service.yaml`
renders a ClusterIP Service `<fullname>-metrics` on port 8080 under the same condition.
`values.yaml` defaults `metrics.enabled` to `true`.

There is **no `FilterProvider`, no `SecureServing` and no authentication filter** anywhere in
`cmd/main.go`. So: anything that can route to the operator pod reads that endpoint, with the
Service or without it — deleting the Service only removes a DNS name. Only `metrics.enabled: false`
closes the port. Adding an authentication filter is a separate trade, because it needs
`TokenReview`/`SubjectAccessReview` grants the ClusterRole does not have.

**D9 — The broker exporter's series and the operator's own series are never merged into one
endpoint.** They have different lifetimes, different failure modes and different readers: one lives
and dies with a broker pod, the other with the operator Deployment.

## Consequences

* **This repository takes on an MQTT client's failure modes.** Reconnection, subscription
  resumption after a broker restart, and a stale-value policy when the broker stops publishing all
  become ours to get right. `paho.mqtt.golang` handles the transport; deciding what a metric reads
  when the connection is down is still a decision we have to make and test.
* **`paho.mqtt.golang` becomes a direct dependency of the operator module,** so its advisories
  become the operator's advisories. The `vuln` target in [`Makefile`](../../Makefile) and the
  `govulncheck` job in [`.github/workflows/release.yml`](../../.github/workflows/release.yml)
  already cover the module graph, so the cost is visibility, not a new pipeline.
* **The operator image grows a second binary that most installs never execute.** Everyone who
  pulls the operator image pays for the exporter, whether or not any `Mosquitto` enables it. That
  is the price of D2, and it is smaller than the price of a second image nobody remembers to
  release.
* **Every broker pod with metrics on grows a container and its resource requests,** on a pod whose
  broker container's own `Resources` come straight from `m.Spec.Resources`.
* **A broken exporter is invisible to Kubernetes** (D5). It has to be noticed as absent metrics,
  which is exactly the signal a broken monitoring stack cannot deliver.
* **The exporter would be the first thing in this repository that speaks MQTT in production — and
  by D5 it is forbidden to act on what it learns.** The broker's probes are
  `TCPSocket` on the listener port
  ([`internal/builder/statefulset.go`](../../internal/builder/statefulset.go)), so a broker that
  accepts connections and refuses every CONNECT still reports Ready; the E2E suite exists partly to
  cover that blind spot. A sidecar holding a real MQTT session would see it immediately. It still
  must not gate readiness, because the failure mode it would introduce — monitoring outage removes
  healthy brokers from their Service — is worse than the one it would close.
* **Adding the sidecar restarts the broker pods.** It changes the pod spec, therefore
  `AnnotationPodSpecHash` (`mko.gtrfc.com/pod-spec-hash`), therefore the pod template, so the
  StatefulSet controller rolls. On a broker without `spec.storage` the persistence directory is an
  `emptyDir`, so retained messages and the persisted store are lost in that roll — and even with
  storage there is no session continuity between pods.
* **`$SYS` is per-pod, so metrics from a multi-replica `Mosquitto` never sum into a cluster
  figure.** Each series describes one independent broker. Any dashboard that adds
  `$SYS/broker/clients/connected` across pods is describing something that does not exist.
* **The mapping table is version-sensitive, so it follows the image pin.** Measured: 2.0.22
  publishes `clients/maximum`, which 2.1.2-alpine does not, and 2.1.2-alpine publishes five topics
  2.0.22 does not. Because `spec.image` lets a user run a broker that is not
  [ADR 0007](0007-one-broker-image-pin-and-why-not-the-openssl-tag.md)'s pin, the exporter must
  treat an unknown topic as skippable and a missing one as absent — **never as zero**, which would
  turn a version difference into a fabricated measurement.
* **The exporter's access depends on the broker's authentication posture,** which
  [ADR 0008](0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md) records
  as anonymous today. It works out of the box now and needs a principal and an ACL entry the day
  authentication lands — and `$SYS/#` is exactly the subscription an ACL would deny first.
* **Anything that can reach the operator pod reads the operator's own metrics** (D8), and no chart
  value except `metrics.enabled: false` changes that. The chart ships no NetworkPolicy, on purpose;
  restricting ingress to the operator namespace is left to the cluster administrator.

## Alternatives Considered

### Depend on `sapcc/mosquitto-exporter`

The closest thing to a standard, and the one that would have made this ADR unnecessary. Rejected:
last code commit 2021-10-25, 22 open issues, five years without a release. Adopting it means
either living with whatever it does not do, or forking it — and a fork is the same ownership as
writing it, with someone else's 2021 design attached.

### Depend on `jnovack/mosquitto-exporter`

Archived. Not a candidate.

### Depend on `hikhvar/mqtt2prometheus` or `kpetremann/mqtt-exporter`

Both maintained, both well-starred, both solving a different problem: they turn **application**
message payloads into metrics. Pointing either at `$SYS` would produce topic-shaped series with no
broker semantics, no types and no handling of `version` or `uptime`, which are strings. Rejected on
model, not on quality.

### Fork one of the single-star projects (`pfinal`, `uhlig-it`, `Alessandrovito`)

Rejected: a fork of a personal project is ownership without the design being ours, and the PHP and
Java ones would each add a runtime this repository does not otherwise contain.

### A central exporter Deployment scraping every broker

Rejected. `$SYS` is broker-local, so it would need to reach every broker pod individually — which
means network access to every data port in every namespace, plus a credential for every
`Mosquitto` the day authentication exists. The sidecar needs neither.

### `kubectl exec` of `mosquitto_sub` in the broker container

Technically available: `test/imagetools` proves `mosquitto_sub` is present in the pinned image, and
the E2E suite already uses it that way. Rejected for production: it needs `pods/exec` on the
operator's cluster-wide ClusterRole, which is the single most powerful verb it could be granted —
`pods/exec` on every namespace is a shell in every pod in the cluster. Trading that for a metrics
feature is not a trade.

### A separate exporter image

Rejected as release surface: a second tag, a second Renovate pin, a second malware scan and a
second thing to forget at release time, in exchange for a few megabytes.

### A readiness probe on the exporter

Rejected — see D5.

### Patch Mosquitto, or write a broker plugin, to expose `/metrics` natively

Rejected outright: this repository consumes the upstream image and does not build one.
`test/imagetools` exists precisely to check assumptions about a filesystem somebody else
maintains. Owning a broker build is a much larger commitment than owning an exporter.

### No broker metrics at all

The status quo, and honestly defensible for a scaffolding release. Rejected as a permanent answer:
an MQTT broker with no visibility into connected clients, dropped publishes and queue depth is one
nobody can operate.

## Residual risks

* **The entire ADR is unimplemented,** and an unimplemented plan can be wrong in ways only code
  finds. Nothing here has been prototyped.
* **The upstream survey is a snapshot.** A maintained `$SYS` exporter appearing before this is
  written would flip D1, and that is the intended trigger to revisit rather than a reason to
  hedge now.
* **The topic sets were measured on two images on one workstation,** idle, with one subscriber,
  over 26 seconds each. Topics that only appear under load, under bridging or under features this
  three-line configuration did not enable are in neither the 55 nor the 51. **Both figures are
  floors, not censuses**, and the 2.0.22-to-2.1.2 delta is therefore a lower bound on the churn a
  mapping table has to absorb, not the whole of it.
* **The `sys_interval` default of 10 s was measured the same way** (three `uptime` messages at
  0 s, 2 s, 12 s) and not read out of the broker's documentation. D7 exists so that the number
  stops being inherited at all.
* **The operator's own metrics endpoint is unauthenticated** (D8). Today its payload is
  controller-runtime's standard series and nothing this repository registers — no Secret material,
  no CR spec content. **That property is not enforced by anything**; the first per-resource metric
  this operator registers changes what an unauthenticated scrape discloses, and this bullet has to
  be rewritten at that moment.
* **The health endpoint (`:8081`) is likewise plain HTTP,** serving `healthz.Ping` for both
  `healthz` and `readyz`.
* **No cluster verification anywhere.** No scrape of the operator endpoint has been observed; the
  `--metrics-bind-address=0` path is pinned by a unit test (`TestManagerOptions`, subtest
  "metrics disabled the controller-runtime way") and by nothing else.

## References

* [`cmd/main.go`](../../cmd/main.go) — `bindOperatorFlags`, `managerOptions`, the absence of any authentication filter
* [`cmd/main_test.go`](../../cmd/main_test.go) — `TestBindOperatorFlags_Defaults`, `TestManagerOptions`
* [`internal/builder/statefulset.go`](../../internal/builder/statefulset.go) — `buildPodSpec` (one container), `DefaultImage`, `TLSVolumeName`, `AnnotationPodSpecHash`, the `TCPSocket` probes
* [`internal/builder/configmap.go`](../../internal/builder/configmap.go) — `GenerateMosquittoConf`, which emits no `sys_interval`
* [`internal/builder/service.go`](../../internal/builder/service.go) — the two Services a readiness failure would remove the pod from
* [`api/v1/mosquitto_types.go`](../../api/v1/mosquitto_types.go) — `MosquittoSpec`, which has no `Metrics` field
* [`config/crd/bases/mko.gtrfc.com_mosquittoes.yaml`](../../config/crd/bases/mko.gtrfc.com_mosquittoes.yaml) — the generated schema, likewise without one
* [`config/rbac/role.yaml`](../../config/rbac/role.yaml) — the ClusterRole the exporter must not widen
* [`Containerfile`](../../Containerfile) — the single-binary build D2 extends
* [`deploy/helm/mosquitto-operator/templates/deployment.yaml`](../../deploy/helm/mosquitto-operator/templates/deployment.yaml), [`deploy/helm/mosquitto-operator/templates/service.yaml`](../../deploy/helm/mosquitto-operator/templates/service.yaml), [`deploy/helm/mosquitto-operator/values.yaml`](../../deploy/helm/mosquitto-operator/values.yaml) — the operator endpoint's exposure
* [`test/imagetools/image_tools_test.go`](../../test/imagetools/image_tools_test.go) — what this repository is willing to assume about the upstream image
* [ADR 0006](0006-both-install-paths-grant-the-same-authority.md) — the authority both install paths grant, which D4 forbids widening
* [ADR 0007](0007-one-broker-image-pin-and-why-not-the-openssl-tag.md) — the `2.1.2-alpine` pin the `$SYS` measurements were taken against
* [ADR 0008](0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md) — the broker's authentication posture the exporter inherits
* [README.md](../../README.md) — the pitch paragraph: each replica is an independent broker process, which is why `$SYS` is per-pod
