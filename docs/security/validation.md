# What validates a `Mosquitto`, and what does not

Which parts of a `Mosquitto` and a `MosquittoUser` the API server checks before they are stored,
which the operator checks when it renders them and where that refusal shows, and which are never
checked at all. What an unchecked `spec.config` or
`spec.image` hands its author is [trust-boundaries.md](trust-boundaries.md); what reaches a running
broker after a change is [rotation.md](rotation.md).

## The schema is the first gate

**There is no admission webhook in this project** — no `ValidatingWebhookConfiguration`, no
`MutatingWebhookConfiguration`, no `config/webhook` directory, nothing in the chart, and no
admission policy of any other kind. What the API server checks is CRD schema validation generated
from the kubebuilder markers in [`api/v1/`](../../api/v1) into
[`config/crd/bases/`](../../config/crd/bases),
which the chart carries byte for byte in
[`templates/crd.yaml`](../../deploy/helm/mosquitto-operator/templates/crd.yaml) (compared
2026-10-05; `make sync-helm-crd` copies it, and the `generated-manifests` job fails on drift).

| Field | What the schema enforces |
|---|---|
| `spec.replicas` | `type: integer`, `format: int32`, `minimum: 1`, `maximum: 9`, `default: 1` |
| `spec.antiAffinity` | `enum: ["off", soft, hard]`, `default: "off"` |
| `spec.tls.secretName` | `minLength: 1`, and `required` within the `tls` object |
| `spec.storage.size` | `minLength: 1`, and `required` within the `storage` object |
| `spec.storage.storageClassName` | `type: string` |
| `spec.image` | `type: string`. Nothing else |
| `spec.config` | `type: string`. No `maxLength`, no `pattern` — the allowlist is checked by the operator, below |
| `spec.podLabels`, `spec.podAnnotations` | `type: object` of strings. Key and value syntax is not checked by the schema; the API server refuses an invalid label on the StatefulSet write, which fails the pass visibly |
| `spec.resources` | The standard `corev1.ResourceRequirements` schema, quantities included |

`TestIntegration_CRD_RejectsInvalidSpecs`
([`test/integration/crd_validation_test.go`](../../test/integration/crd_validation_test.go)) pins
four of those against a real API server: ten replicas, an unknown anti-affinity mode, an empty TLS
Secret name and an empty storage size are all refused on `Create`. Passing against envtest on
2026-10-05.

For a `MosquittoUser` ([`mko.gtrfc.com_mosquittousers.yaml`](../../config/crd/bases/mko.gtrfc.com_mosquittousers.yaml)):

| Field | What the schema enforces |
|---|---|
| `spec.brokerRef.name`, `spec.credentialsSecret.name` | required, 1 to 253 characters |
| `spec.credentialsSecret.usernameKey`, `.passwordKey` | default `username` / `password`, pattern `^[-._a-zA-Z0-9]+$` |
| `spec.acls` | at most 256 entries |
| `spec.acls[].topic` | 1 to 1024 characters; CEL: does not start with `$`, contains no control character |
| `spec.acls[].access` | `enum: [read, write, readwrite]` |

`TestIntegration_CRD_UserRefusesWhatCELCanSee` pins the `$SYS` and `$CONTROL` refusals, an unknown
access mode, a line break and an empty topic against a real API server, and was observed failing
with the `$` rule removed (`An error is expected but got nil`).

Two helpers keep a half-filled spec from producing something worse if the schema is ever bypassed:
`IsTLSEnabled()` is false for an empty `secretName`, so no listener is generated with no certificate
to serve, and `AntiAffinityMode()` treats any unknown value as `off`, the weakest setting.

## What the operator refuses when it renders

The username lives in a Secret, where no schema can see it, and a schema can be older than the
operator or bypassed, so the operator checks again when it renders — this is the authority, the CRD
the shape check ([ADR 0013](../adr/0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md)
D4, D5; [`internal/auth/render.go`](../../internal/auth/render.go)):

- **The username** must match `^[A-Za-z0-9][A-Za-z0-9._@-]{0,127}$` — an allowlist, so a `:` or a
  line break can never split or add a line of the `passwd` file — and must not start with `mko-`
  in any case: `UsernameInvalid`, `UsernameReserved`.
- **The password** must not be empty: `PasswordEmpty`.
- **Every ACL topic** must not start with `$`, contain a control character or start or end with
  whitespace, and `#` and `+` must be whole levels (`#` only last): `TopicRefused`. An inner space
  is allowed; the `acl-file` parser reads the rest of the line as the topic (broker-behaviour.md
  M27).
- **Two users with one username**: the oldest keeps it, the others get `UsernameConflict`.

A refused user is left out of `<broker>-auth` — never trimmed or half-rendered — and reports the
reason on itself; the broker and the other users are not affected (`TestRender_Refusals`,
`TestReconcile_EveryUserReason`).

**`spec.config` is checked line by line against an allowlist** of 26 tuning directives
([ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
D15, `builder.ValidateSpecConfig`, taken from `mosquitto.conf(5)` of the pinned 2.1.2 — M26). A
line whose first word is not on it refuses the whole pass before anything is written: `phase:
Failed`, reason `ConfigDirectiveRefused`, the message naming the line by its number within
`spec.config` (`TestValidateSpecConfig`, `TestReconcile_ARefusedConfigWritesNothing`; on Kind a
second listener was refused while the broker kept serving).

## What fails later, and where it shows

- **An unparsable `spec.storage.size`.** `minLength: 1` is all the schema knows; the value is parsed
  by `resource.ParseQuantity` in `buildVolumeClaimTemplates`, which fails the pass — `phase: Failed`,
  deliberately visible, because the value reaches an immutable claim template
  (`TestBuildStatefulSet_UnparsableStorageSizeFails`, `TestReconcile_UnbuildableSpecFailsVisibly`).
- **A value the broker refuses.** The `config-check` init container runs the broker's own
  `--test-config` on the generated file before the broker starts
  ([ADR 0007](../adr/0007-one-broker-image-pin-and-why-not-the-openssl-tag.md) D10): a bad value of
  an allowed directive in `spec.config` (`max_qos 7`), or a generated directive an older image
  lacks, leaves the pod in `Init:Error` with the broker's message, file and line in the init
  container's log, while the resource reconciles cleanly
  (`TestE2E_ConfigCheck_StopsATypoBeforeTheBroker`, observed on Kind).
  `--test-config` knows directive names only: a value a plugin refuses, or a combination the
  broker refuses at start, still passes it and ends in a `CrashLoopBackOff` of the broker
  ([broker-behaviour.md](../developer/broker-behaviour.md) M8).
- **A TLS Secret without consent, with `secretSecurity: true`.** The operator reads the Secret's
  labels before writing anything and refuses one without `mko.gtrfc.com/consumable=true`, or one
  that does not exist: `phase: Failed`, reason `SecretNotConsumable` or `SecretNotFound`.
- **A missing or broken TLS Secret, at the default.** The operator does not read the Secret
  ([credentials.md](credentials.md#what-the-operator-holds)), so neither its
  existence, nor its keys, nor whether key and certificate match is checked. A `secretName` that
  names nothing produces a StatefulSet that reconciles cleanly and a pod that stays in
  `ContainerCreating` with a `FailedMount` event (observed on Kind; `TestIntegration_TLS_DoesNotWaitForTheSecret`
  asserts the reconcile half);
  [`test/integration/tls_test.go`](../../test/integration/tls_test.go) mounts a Secret whose values
  are the literal strings `not-a-certificate` and `not-a-key`, and the operator is content — envtest
  runs no kubelet, so nothing ever parses them. What the pod shows in each case is kubelet and broker
  behaviour and was not observed on a cluster.

## What this does not cover

### The values in `spec.config`, and `spec.image`

The operator checks which directives `spec.config` names, not their values: validating values
would mean reimplementing Mosquitto's configuration parser for an image this repository consumes and
does not build — the `config-check` init container lets the broker's own parser speak instead, after
the resource is stored, and M8 shows it does not catch every bad value. `spec.image` takes any registry, any tag, any digest, with no allowlist. What both hand their
author is [trust-boundaries.md H-2](trust-boundaries.md#h-2).

### Cross-field rules

None on a `Mosquitto`: a grep for `x-kubernetes-validations` over its rendered CRD returns nothing.
The `MosquittoUser` CRD carries two CEL rules, both on a single field (`spec.acls[].topic`).

### The resource name

Kubebuilder markers cannot constrain `metadata.name`, and every managed name and the
`app.kubernetes.io/instance` label value are derived from it: `<name>`, `<name>-headless`,
`<name>-config`, `<name>-auth`. A name the API server accepts for a `Mosquitto` but refuses for a derived object or
a label value fails the pass visibly; which names those are, and what the failure looks like, was
not tested here.
