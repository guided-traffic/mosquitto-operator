# What validates a `Mosquitto`, and what does not

Which parts of a `Mosquitto` the API server checks before it is stored, which fail later and where
that failure shows, and which are never checked at all. What an unchecked `spec.config` or
`spec.image` hands its author is [trust-boundaries.md](trust-boundaries.md); what reaches a running
broker after a change is [rotation.md](rotation.md).

## The schema is the only gate

**There is no admission webhook in this project** — no `ValidatingWebhookConfiguration`, no
`MutatingWebhookConfiguration`, no `config/webhook` directory, nothing in the chart, and no
admission policy of any other kind. Everything that validates a `Mosquitto` is CRD schema
validation generated from the kubebuilder markers in
[`api/v1/mosquitto_types.go`](../../api/v1/mosquitto_types.go) into
[`config/crd/bases/mko.gtrfc.com_mosquittoes.yaml`](../../config/crd/bases/mko.gtrfc.com_mosquittoes.yaml),
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
| `spec.config` | `type: string`. No `maxLength`, no `pattern` |
| `spec.resources` | The standard `corev1.ResourceRequirements` schema, quantities included |

`TestIntegration_CRD_RejectsInvalidSpecs`
([`test/integration/crd_validation_test.go`](../../test/integration/crd_validation_test.go)) pins
four of those against a real API server: ten replicas, an unknown anti-affinity mode, an empty TLS
Secret name and an empty storage size are all refused on `Create`. Passing against envtest on
2026-10-05.

Two helpers keep a half-filled spec from producing something worse if the schema is ever bypassed:
`IsTLSEnabled()` is false for an empty `secretName`, so no listener is generated with no certificate
to serve, and `AntiAffinityMode()` treats any unknown value as `off`, the weakest setting.

## What fails later, and where it shows

- **An unparsable `spec.storage.size`.** `minLength: 1` is all the schema knows; the value is parsed
  by `resource.ParseQuantity` in `buildVolumeClaimTemplates`, which fails the pass — `phase: Failed`,
  deliberately visible, because the value reaches an immutable claim template
  (`TestBuildStatefulSet_UnparsableStorageSizeFails`, `TestReconcile_UnbuildableSpecFailsVisibly`).
- **A `spec.config` the broker rejects.** The broker sees the file first at startup, so a rejected
  configuration is a `CrashLoopBackOff` of the broker pods while the resource reconciles cleanly.
- **A missing or broken TLS Secret.** The operator cannot read the Secret
  ([credentials.md](credentials.md#the-operator-holds-no-workload-credential)), so neither its
  existence, nor its keys, nor whether key and certificate match is checked. A `secretName` that
  names nothing produces a StatefulSet that reconciles cleanly and pods that never start
  (`TestIntegration_TLS_DoesNotWaitForTheSecret`);
  [`test/integration/tls_test.go`](../../test/integration/tls_test.go) mounts a Secret whose values
  are the literal strings `not-a-certificate` and `not-a-key`, and the operator is content — envtest
  runs no kubelet, so nothing ever parses them. What the pod shows in each case is kubelet and broker
  behaviour and was not observed on a cluster.

## What this does not cover

### `spec.config` and `spec.image`

Nothing validates either. For `spec.config` that is a deliberate non-goal: validating it means
reimplementing Mosquitto's configuration parser for an image this repository consumes and does not
build ([ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
D9). `spec.image` takes any registry, any tag, any digest, with no allowlist. What both hand their
author is [trust-boundaries.md H-2](trust-boundaries.md#h-2).

### Cross-field rules

There are none. A grep for `x-kubernetes-validations` over the rendered CRD returns nothing; the only
`x-kubernetes-*` keys present come from the embedded `ResourceRequirements` schema.

### The resource name

Kubebuilder markers cannot constrain `metadata.name`, and every managed name and the
`app.kubernetes.io/instance` label value are derived from it: `<name>`, `<name>-headless`,
`<name>-config`. A name the API server accepts for a `Mosquitto` but refuses for a derived object or
a label value fails the pass visibly; which names those are, and what the failure looks like, was
not tested here.
