# ADR 0008: The Generated Broker Is Anonymous, and `spec.config` Can Undo the Rest

## Status

Accepted. Date: 2026-09-01. Both decision groups are **implemented** as described; the future work
named in D11 and D12 is not.

Verified by reading, on 2026-09-01:

* [`internal/builder/configmap.go`](../../internal/builder/configmap.go) in full — `GenerateMosquittoConf`
  appends `allow_anonymous true` **unconditionally**, on both the TLS and the plaintext branch, and
  appends `m.Spec.Config` after it whenever `strings.TrimSpace(m.Spec.Config) != ""`. The scoping
  of the TLS guarantee is stated in the doc comment on `BrokerPort`.
* [`api/v1/mosquitto_types.go`](../../api/v1/mosquitto_types.go) — `MosquittoSpec.Config`,
  `MosquittoSpec.TLS`, `MosquittoTLS.SecretName`, `IsTLSEnabled`. The type doc comment states the
  anonymous posture; the API declares no authentication field of any kind.
* [`internal/builder/configmap_test.go`](../../internal/builder/configmap_test.go) —
  `TestGenerateMosquittoConf_PlainListener`, `TestGenerateMosquittoConf_TLSReplacesThePlainListener`,
  `TestGenerateMosquittoConf_SpecConfigIsAppendedVerbatim` (whose "a user override lands after the
  generated line" case is literally `allow_anonymous false`, asserted by string index against
  `allow_anonymous true`).
* [`test/e2e/tls_test.go`](../../test/e2e/tls_test.go) — the cert-manager path, the read-only mount,
  `require.Len(t, container.Ports, 1, "TLS replaces the plain listener, it does not add to it")`,
  and the assertion that the operator creates no `Certificate` of its own.
* [`test/integration/tls_test.go`](../../test/integration/tls_test.go) —
  `TestIntegration_TLS_MountsTheSecretAndMovesTheListener` and
  `TestIntegration_TLS_DoesNotWaitForTheSecret`.
* [`internal/builder/statefulset.go`](../../internal/builder/statefulset.go) — the single container
  port, the `TCPSocket` readiness and liveness probes, the read-only TLS mount.
* [`internal/controller/mosquitto_controller.go`](../../internal/controller/mosquitto_controller.go)
  — `SetupWithManager` owns `StatefulSet`, `ConfigMap` and `Service` and **watches no `Secret`**;
  the kubebuilder RBAC markers grant nothing on `secrets`.
* [`config/crd/bases/mko.gtrfc.com_mosquittoes.yaml`](../../config/crd/bases/mko.gtrfc.com_mosquittoes.yaml)
  — `config` is `type: string` with no `maxLength` and no `pattern`. A grep for `webhook` over the
  tracked files finds **no admission webhook of this operator**: the only hits anywhere are
  `kubectl wait --for=condition=Available deployment/cert-manager-webhook` in
  [`Makefile`](../../Makefile) and [`.github/workflows/release.yml`](../../.github/workflows/release.yml),
  which wait on cert-manager's own webhook during the E2E install. A grep for `NetworkPolicy` finds
  only two chart comments explaining that none is shipped.

Verified by **measurement**, against the pinned image `eclipse-mosquitto:2.1.2-alpine`
(`DefaultImage` in `internal/builder/statefulset.go`), run locally with `docker run --entrypoint sh`
on 2026-09-01:

* A configuration of `log_dest stdout` + `listener 1883` and **no** `allow_anonymous` line starts
  fine and then refuses every client: `mosquitto_pub` returns
  `Connection error: Connection Refused: not authorised`, exit status 5, and the broker logs
  `New connection from 127.0.0.1:52682 on port 1883.` followed by
  `Client auto-… disconnected: not authorised.`
* The same configuration plus `allow_anonymous true` accepts the publish, exit status 0.
* **The refusal happens after the TCP accept.** The broker log line above proves the connection was
  established before the CONNECT was rejected — which is exactly what the readiness probe checks.

Not verified: nothing in this repository has ever run against a real cluster. The E2E suite exists
in the tree and was **not** executed for this ADR. The claim that `password_file` and `acl_file`
are deprecated in Mosquitto 2.1 and removed in 3.0 is an upstream statement repeated in the code
comments and in [`README.md`](../../README.md); it was not verified against upstream sources here.

## Context

Mosquitto 2.x refuses clients on a listener that has no configured authentication. That is not a
posture choice, it is the broker's behaviour, and the measurement above pins it on the exact image
this operator runs.

Against that stands the API: `MosquittoSpec` models `Replicas`, `Image`, `Config`, `AntiAffinity`,
`TLS`, `Resources` and `Storage`. **It models no authentication at all** — no password Secret, no
ACL file, no user list. So the config generator faces a binary choice with no third option
available in the current API:

1. emit nothing about anonymity, and ship a broker that rejects every client — a default resource
   that serves nobody;
2. emit `allow_anonymous true`, and ship a broker that serves everybody.

Option 1 fails silently in the worst possible way. The readiness probe is
`TCPSocket` on the listener port, and the measurement above shows the broker completes the TCP
accept before rejecting the CONNECT. So a broker in state 1 passes its probe, becomes an endpoint
of both Services, and drives `status.phase` to `Ready` — a `Mosquitto` reporting `Ready` while
every client is turned away. The E2E tier exists partly to catch that class of failure
([`test/e2e/mosquitto_test.go`](../../test/e2e/mosquitto_test.go): "a broker that accepts
connections and then rejects every CONNECT … still reports Ready").

The second force is `spec.config`. A minimal CRD cannot model every broker option, so there is an
escape hatch: a free-form string appended to the generated file. That hatch is what makes the
operator usable before the API is complete, and it is also what makes every guarantee the
generator states conditional.

## Decision

### Group A — the posture, stated without euphemism

**D1 — The generated `mosquitto.conf` enables anonymous access, always.**
`GenerateMosquittoConf` appends `allow_anonymous true` on every path, TLS or not. Stated plainly:
**every broker this operator provisions accepts publish and subscribe from anything that can open
a TCP connection to it.** Not "by default in some configurations" — on every `Mosquitto` resource
the current API can express, unless the user closes it themselves through `spec.config` (D6).

**D2 — The generated file says so, in the file.** The block carries the reason inline:
"This broker accepts anonymous clients: the CRD models no authentication, and Mosquitto 2.x would
otherwise reject every client." A `kubectl get configmap <name>-config -o yaml` is a complete
answer about the posture; the reader does not have to find this ADR.

**D3 — `spec.tls` changes the transport, not the trust.** With `IsTLSEnabled()` the generated block
emits `listener 8883`, `certfile /mosquitto/tls/tls.crt` and `keyfile /mosquitto/tls/tls.key`, and
nothing else. **No `require_certificate` is rendered anywhere in this repository** — the identifier
appears only in doc comments saying it is absent. So TLS gives confidentiality and authenticates
*the broker to its clients*; it authenticates **no client to the broker**. An encrypted anonymous
broker is still an anonymous broker.

This restates [ADR 0001](0001-the-operator-consumes-tls-material-it-never-issues-it.md) D9 in this
family's terms rather than deciding anything new, because it is the sentence that gets dropped:
"we enabled TLS" is the most common stand-in for "we secured the broker", and here the two have
almost nothing to do with each other.

**D4 — The TLS material is consumed, never owned, and never watched — and that family is
[ADR 0001](0001-the-operator-consumes-tls-material-it-never-issues-it.md)'s, not this one's.**
Recorded here only for what it costs the posture: the operator neither creates the Secret
(`TestIntegration_TLS_DoesNotWaitForTheSecret`) nor a cert-manager `Certificate`
(`TestE2E_TLS_CertManagerIssuedSecretServesMQTTS`), `SetupWithManager` registers no watch on
`Secret`, and the ClusterRole grants nothing on `secrets`. **A rotation therefore reaches running
pods only when they restart** — see ADR 0001 D6 and D7 for the rule and its open half.

**D5 — The posture is repeated at every surface a user meets, not only here.** A posture documented
in one place is a posture most users never read. Counted rather than asserted — a grep for
`anonymous` finds it stated on four surfaces:

| Surface | How a user meets it |
|---|---|
| The `MosquittoSpec` type doc comment ([`api/v1/mosquitto_types.go`](../../api/v1/mosquitto_types.go)) | Reading the API types |
| The CRD schema description, carrying that comment verbatim in both [`config/crd/bases/mko.gtrfc.com_mosquittoes.yaml`](../../config/crd/bases/mko.gtrfc.com_mosquittoes.yaml) and [`deploy/helm/mosquitto-operator/templates/crd.yaml`](../../deploy/helm/mosquitto-operator/templates/crd.yaml) | `kubectl explain mosquitto.spec` |
| The generated file's own comments ([`internal/builder/configmap.go`](../../internal/builder/configmap.go)) | `kubectl get configmap <name>-config -o yaml` |
| [`README.md`](../../README.md), "Two modes, and what each one protects" | Arriving at the project |

The field comment on `MosquittoTLS.SecretName` points at the type documentation ("It does not
authenticate clients: see the type documentation") rather than restating it, which is the right
shape — one authority, referenced.

**The gap this counting exposed:** the chart's
[`values.yaml`](../../deploy/helm/mosquitto-operator/values.yaml) carries security notes about the
operator's metrics endpoint and about TLS material, and **says nothing about the anonymous
posture**. Someone whose whole encounter with this project is `helm install` plus reading the
values file never meets D1. That is an open item in Residual risks, not a claim that D5 is
currently satisfied everywhere.

### Group B — `spec.config` is appended verbatim, and wins

**D6 — `spec.config` goes in last, unmodified, and a repeated global option therefore overrides the
generated one.** The only transformations are `strings.TrimSpace` on the emptiness check and
`strings.TrimRight(m.Spec.Config, "\n")` on the content. It is preceded by the marker comment
`# spec.config, appended verbatim.`, so the boundary between operator output and user input is
visible in the rendered file. `TestGenerateMosquittoConf_SpecConfigIsAppendedVerbatim` pins the
ordering by string index, with `allow_anonymous false` as one of its cases — **overriding the
anonymous default is a supported, tested use of the field, not a loophole.**

**D7 — `spec.config` can declare listeners, bridges and log destinations the operator does not
model, and the operator does not learn about them.** The container port list and both Service port
lists come from `BrokerPort(m)` / `BrokerPortName(m)`, which read `IsTLSEnabled()` and nothing
else. A listener declared in `spec.config` is served by the broker process and exposed by no
Kubernetes object.

**D8 — "Enabling TLS closes the plaintext port" is a guarantee about the *generated block*, not
about the *file*.** [ADR 0001](0001-the-operator-consumes-tls-material-it-never-issues-it.md) D4
decides that the generated block declares exactly one listener either way, so `spec.tls` moves the
listener rather than adding one; **this decision is the scope on that guarantee, and the scope is
the part that matters here.** The one-listener half is what
`TestGenerateMosquittoConf_TLSReplacesThePlainListener` asserts (`listener 8883` present,
`listener 1883` absent), what the integration and E2E tests assert on the container and Service
ports, and what the doc comment on `BrokerPort` says in as many words: *"It says nothing about the
file as a whole."* A `spec.config` containing `listener 1883` reopens plaintext, and **the missing
container port declares nothing about reachability**: `containerPort` is documentation, not a
firewall, so any pod that can route to the broker pod's IP reaches any port the process is
listening on. **This scoping is the single most important sentence in this ADR**, because the
unscoped version of it is the one everybody remembers.

**D9 — Nothing validates `spec.config`, and that is a deliberate non-goal.** The CRD types it as
`string` with no `maxLength` and no `pattern`; no admission webhook exists in this repository. The
broker sees the file for the first time at startup, so a rejected configuration is a
`CrashLoopBackOff`, not a rejected `kubectl apply`. Validating it would mean reimplementing
Mosquitto's configuration parser and keeping it in step with an image this repository consumes and
does not build.

**D10 — Anyone who can create or update a `Mosquitto` in a namespace controls that broker's entire
configuration file.** `create`/`update` on `mosquittoes.mko.gtrfc.com` in a namespace is, in
practice, authority to write `mosquitto.conf` — to open listeners, to bridge messages to an
external broker, to redirect logs. **For a cluster operator this means RBAC on `mosquittoes` is the
control surface for `spec.config`, and there is no second one.** There is no webhook to constrain
it, no allowlist of permitted directives, and no field-level RBAC in Kubernetes.

**D11 — Authentication, when it arrives, is modelled in the API and rendered as the 2.1 plugin
form.** Not `password_file`: on the pinned image that option is deprecated and it is removed in
3.0, so generating it into every user's cluster would buy a migration to run later with users
attached. The generator emits the `password-file` plugin instead. This is
[ADR 0007](0007-one-broker-image-pin-and-why-not-the-openssl-tag.md) D7's obligation coming due —
that ADR fixed the rule when it chose 2.1 over 2.0; this one names which feature it binds. **The
field is the decision, not the rendering**: a credential has to be modelled as a Secret reference,
never as `spec.config` content, for the reason in Consequences. Not implemented.

**D12 — Authorization is an ACL plugin, and it is a separate decision from D11.** Same reasoning:
the `acl-file` plugin, not `acl_file`. Authenticated-but-unrestricted is a different posture from
anonymous and needs its own field. Not implemented.

## Consequences

* **Every broker this operator ships today is open to anything that can route to it.** With one
  replica and no `spec.config`, the ClusterIP Service in front of `1883` is reachable from every
  pod in the cluster that has network access to that namespace — and this repository ships no
  NetworkPolicy, on purpose (a policy fitting one cluster's CNI and topology fits few others), so
  the operator provides nothing that narrows it. Network reach is the only boundary.
* **`status.phase: Ready` is a statement about TCP, not about MQTT.** Both probes are `TCPSocket`.
  That is what makes D1 preferable to the silent alternative, and it is also a permanent limit on
  what the phase means.
* **A user who puts a credential into `spec.config` publishes it twice.** It stays in the CR spec —
  readable by anyone with `get mosquittoes` in the namespace, and in every GitOps diff and backup —
  and `BuildConfigMap` copies the rendered file into the ConfigMap `<name>-config`, readable by
  anyone with `get configmaps` there. **ConfigMaps are not Secrets:** not covered by a Secret-only
  KMS configuration, and visible to a wider set of subjects. Any future credential surface must not
  travel this path.
* **`spec.config` is unbounded.** No `maxLength` on the field means the only limits are the API
  server's request-size and etcd's object-size limits, and the whole string is rendered into a
  ConfigMap the broker pods mount.
* **TLS gives users a false sense of closure if D3 is not read.** "We enabled TLS" is a common
  stand-in for "we secured the broker". Here it means the wire is encrypted and the broker proved
  its name, on a broker that still accepts every client.
* **The TLS guarantee in D8 is true of what the operator writes and false of what the file can
  contain,** so any future documentation, alerting or compliance statement derived from "TLS means
  no plaintext port" is wrong for a resource that uses `spec.config`. Nothing detects that case.
* **A certificate rotation is a manual roll** (D4). The operator does not watch the Secret, so
  running pods keep serving the material they started with for as long as they live.
* **Adding authentication later is not a drop-in change.** Whatever principal the brokers get, the
  metrics sidecar decided in
  [ADR 0002](0002-the-metrics-exporter-is-written-here.md) needs one too — `$SYS/#` is precisely
  the subscription an ACL denies first.
* **Turning `allow_anonymous` off is a `spec.config` edit that rolls the pods,** because
  `AnnotationConfigHash` (`mko.gtrfc.com/config-hash`) digests the generated file and is part of
  the pod template. That is the desired behaviour — Mosquitto reads its configuration once at
  startup — and it means a security change costs a broker restart.

## Alternatives Considered

### Emit nothing, and let Mosquitto's own default (deny) stand

Rejected, and this is the alternative that most deserved to win. It fails closed, which is normally
the right instinct. It loses on one measured fact: the deny happens *after* the TCP accept, so the
`TCPSocket` readiness probe passes and the resource reports `Ready`. Every default `Mosquitto`
would look healthy and serve nobody, with the failure visible only in broker logs. **Failing closed
is only a virtue when the failure is visible**, and here it is not — and there is no field in the
current API a user could set to fix it. The right time to revisit this is D11, not before.

### Ship `allow_anonymous false` and require every user to write `spec.config`

Same failure mode as above, plus it makes the minimal example in the README a non-working one.
Rejected. It becomes the obvious posture once D11 exists, because then there is something to
configure instead of an escape hatch.

### Generate a password file and a random credential per resource

Rejected for this step, and it is a larger decision than it looks: the operator would own a
credential lifecycle — generation, exposure to clients, rotation, deletion on CR removal — and
would need `create` on `secrets`, which the ClusterRole deliberately does not have. It would also
have to reach clients somehow, which is the part nobody solves for the user.

### Model authentication now, in this API

Rejected as scope, not as direction — D11 and D12 are that direction. Doing it here would mean
choosing the plugin configuration shape before the generator, the tests and the image checks
support it.

### Parse and validate `spec.config` in the operator

Rejected. It is Mosquitto's parser, for an image this repository does not build, whose option set
moves between versions — `password_file` and `acl_file` are exactly the kind of option that changes
under a validator's feet. A validator that is wrong in either direction is worse than none: it
either rejects valid configurations or blesses invalid ones.

### Restrict `spec.config` to an allowlist of modelled directives

Rejected: it converts the escape hatch into a second, weaker API, and every option a user actually
needs arrives as a feature request against the allowlist rather than against the CRD.

### An admission webhook constraining `spec.config`

Rejected for this step. A webhook is a certificate, an availability dependency in front of every
write to the resource, and a failure mode of its own; and it would still be enforcing a policy
nobody has written yet. If `spec.config` is ever to be constrained, the policy comes first.

### Refuse `spec.config` entirely and model every option

Rejected: it makes the operator unusable for anything the CRD has not yet grown a field for, which
today is almost everything.

### Have `spec.tls` add an MQTTS listener alongside the plaintext one

Rejected as a silent downgrade. TLS that leaves the cleartext port open is TLS for whoever
remembers to reconfigure their clients. Keeping the generated block at exactly one listener is also
what makes the single container port and the single Service port honest.

### Require client certificates under TLS (`require_certificate true`)

Not taken, and it is a genuine hardening item rather than a rejected idea. It would force every
client of every broker to hold issued material this operator does not manage, and there is no field
in the API to opt into it.

## Residual risks

* **Anonymous access is the shipped posture (open).** It is documented on four surfaces and closed
  by nothing. D11 is the only real answer; until then the mitigation is network policy the cluster
  administrator writes and RBAC on who may create a `Mosquitto` at all.
* **The chart's `values.yaml` does not state the anonymous posture (open, and cheap to fix).**
  Verified by grep: `anonymous` appears zero times in
  [`deploy/helm/mosquitto-operator/values.yaml`](../../deploy/helm/mosquitto-operator/values.yaml),
  which does carry security notes for the metrics endpoint and for TLS. D5 is therefore not
  satisfied on the surface a Helm-only user is most likely to read.
* **`spec.config` is namespace-scoped, unvalidated privilege over the broker's whole configuration
  (open, accepted).** The bridge case is the sharpest: a `spec.config` bridge block forwards
  messages to an external broker, and nothing in this repository would show it anywhere except in
  the CR the user wrote.
* **The "TLS closes the plaintext port" guarantee can be silently voided (open).** Nothing detects
  a `listener` line in `spec.config`, and no test asserts the negative case — the existing tests
  cover the generated block only.
* **No NetworkPolicy is shipped for broker pods,** so the network boundary the whole posture leans
  on is entirely the administrator's to build.
* **Not verified against a cluster.** Every claim about how the *operator* behaves comes from
  reading the code and from tests that exist in the tree; the E2E suite was not run for this ADR.
  Every claim about how the *broker* behaves was measured against the pinned image on a local
  workstation, not inside Kubernetes — so kubelet mount semantics, Service routing and the readiness
  probe's real behaviour on a cluster are inferred, not observed.
* **The `password_file` / `acl_file` deprecation is repeated from upstream, unverified here.** It is
  load-bearing for D11 and D12: if it is wrong, those decisions pick a harder implementation than
  necessary — which is the safe direction to be wrong in, but it is still unverified.
* **`spec.config` has no size limit**, and no test covers what a very large value does to the
  ConfigMap or the pod's mounted volume.

## References

* [`internal/builder/configmap.go`](../../internal/builder/configmap.go) — `GenerateMosquittoConf`, `BrokerPort`, `BrokerPortName`, `BuildConfigMap`, `ConfigKey`, `TLSMountPath`, `TLSCertKey`, `TLSKeyKey`
* [`internal/builder/configmap_test.go`](../../internal/builder/configmap_test.go) — the three generator tests, including the `allow_anonymous false` override case
* [`api/v1/mosquitto_types.go`](../../api/v1/mosquitto_types.go) — `MosquittoSpec`, `MosquittoTLS`, `IsTLSEnabled`, and the posture documented on the type
* [`internal/builder/statefulset.go`](../../internal/builder/statefulset.go) — the single container port, the `TCPSocket` probes, `AnnotationConfigHash`
* [`internal/builder/service.go`](../../internal/builder/service.go) — the single Service port both Services expose
* [`internal/controller/mosquitto_controller.go`](../../internal/controller/mosquitto_controller.go) — `SetupWithManager` (no `Secret` watch), the RBAC markers (no `secrets` rule)
* [`config/rbac/role.yaml`](../../config/rbac/role.yaml) — the generated ClusterRole
* [`config/crd/bases/mko.gtrfc.com_mosquittoes.yaml`](../../config/crd/bases/mko.gtrfc.com_mosquittoes.yaml) — `config` as an unconstrained string
* [`test/integration/tls_test.go`](../../test/integration/tls_test.go) — `TestIntegration_TLS_MountsTheSecretAndMovesTheListener`, `TestIntegration_TLS_DoesNotWaitForTheSecret`
* [`test/e2e/tls_test.go`](../../test/e2e/tls_test.go) — the cert-manager path and "the operator creates no Certificate of its own"
* [`test/e2e/mosquitto_test.go`](../../test/e2e/mosquitto_test.go) — why a TCP probe is not an MQTT statement
* [`deploy/helm/mosquitto-operator/values.yaml`](../../deploy/helm/mosquitto-operator/values.yaml) — the TLS and NetworkPolicy notes the chart carries
* [README.md](../../README.md) — the user-facing statement of the same posture
* [ADR 0001](0001-the-operator-consumes-tls-material-it-never-issues-it.md) — the TLS family this ADR scopes rather than restates: D4 (one listener), D6 and D7 (no watch, restart to rotate), D9 (server authentication only)
* [ADR 0002](0002-the-metrics-exporter-is-written-here.md) — the metrics sidecar that inherits this posture and will need a principal under D11
* [ADR 0007](0007-one-broker-image-pin-and-why-not-the-openssl-tag.md) — why the pin is `2.1.2-alpine`, which is what makes the plugin form in D11 and D12 the right target and `password_file` the wrong one
