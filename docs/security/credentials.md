# Where credentials and broker data live

Which credentials and which data the operator and its brokers handle on a cluster, the path each
one takes, and the places a credential can end up without anybody meaning it to. The credentials of
the build and release pipeline are [ci-and-supply-chain.md](ci-and-supply-chain.md); what reaches a
running broker after a change, a rotated certificate included, is [rotation.md](rotation.md).

## The operator holds no workload credential

There is no rule on `secrets` in either install path
([privilege-footprint.md](privilege-footprint.md#what-is-absent)). `SetupWithManager` registers
`For(&mkov1.Mosquitto{})` plus `Owns` on `appsv1.StatefulSet`, `corev1.ConfigMap` and
`corev1.Service` — no `Watches`, and nothing on `Secret`
([`internal/controller/mosquitto_controller.go`](../../internal/controller/mosquitto_controller.go)).
Not watching the TLS Secret is the privilege boundary, not an oversight
([ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md) D6), and
[rotation.md](rotation.md) states what it costs. The one credential the operator process holds is
its own ServiceAccount token ([privilege-footprint.md H-5](privilege-footprint.md#h-5)).

## TLS material

| Step | What happens | Read from |
|---|---|---|
| The Secret is created | By hand (`kubectl create secret tls`) or by a cert-manager `Certificate` the administrator owns. **Never by this operator** — no cert-manager dependency in [`Chart.yaml`](../../deploy/helm/mosquitto-operator/Chart.yaml), no cert-manager module in [`go.mod`](../../go.mod), and `TestIntegration_TLS_DoesNotWaitForTheSecret` asserts the operator creates none | [ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md) D1–D3 |
| The pod references it | `buildPodSpec` adds a `corev1.SecretVolumeSource` with `SecretName: m.Spec.TLS.SecretName`, `DefaultMode` `0o644` and **no `Items` projection**; the Secret resolves in the resource's own namespace | [`internal/builder/statefulset.go`](../../internal/builder/statefulset.go) |
| The kubelet mounts it | At `TLSMountPath = "/mosquitto/tls"`, `ReadOnly: true`. Because there is no projection, **every key the Secret carries appears in that directory**, not only the two the configuration names — a cert-manager Secret's `ca.crt` among them | [`internal/builder/configmap.go`](../../internal/builder/configmap.go), [`internal/builder/statefulset.go`](../../internal/builder/statefulset.go) |
| The broker reads it | The generated block names `certfile /mosquitto/tls/tls.crt` and `keyfile /mosquitto/tls/tls.key` — `TLSCertKey` and `TLSKeyKey` — read when the process starts | [`internal/builder/configmap.go`](../../internal/builder/configmap.go) |

The private key therefore travels kubelet → volume → broker process and never passes through the
operator. The mount itself is kubelet behaviour and was not observed on a cluster here; the
integration tier shows only that the API server accepts the StatefulSet that asks for it.

## Broker data at rest

The generated file sets `persistence true` with `persistence_location /mosquitto/data/`, so the
broker writes its retained messages and session state into the `data` volume: a PersistentVolumeClaim
from the `data` template when `spec.storage` is set, an `emptyDir` otherwise. The operator encrypts
nothing; what protects that data at rest is the storage class's, and the claims outlive the
resource ([tenancy.md H-12](tenancy.md#h-12)).

## Where credentials can end up by accident

- **`spec.config` is copied into a ConfigMap** — [H-14](#h-14).
- **Not into status, and not into Events.** The reconciler writes phase, ready replicas, observed
  generation and one `Ready` condition into `.status`; the condition message on a failed pass is the
  error text — the operator's own names objects and, for an unparsable storage size, that value; an
  API server's refusal can quote the value it refused. It records **no Events at all**: no `EventRecorder`, `Recorder` or `Eventf` appears in
  `internal/`, `cmd/` or `api/`. The `events` rule that does exist belongs to client-go's
  leader-election `LeaseLock`
  ([`config/rbac/leader_election_role.yaml`](../../config/rbac/leader_election_role.yaml)).
- **Broker logs go to stdout.** The generated file sets `log_dest stdout` with `log_type` `error`,
  `warning`, `notice` and `information`, so broker output is whatever `kubectl logs` shows and
  inherits the cluster's log retention and readers. At that level the broker logs every connection
  with its source address and client id (ADR 0008's measurement record shows such lines); what else
  appears once `spec.config` adds authentication or bridges was not measured.
- **The operator's own log** carries resource and object names on every create and update; the
  reconciler's own log lines carry no spec content, and a failed pass's error is logged by
  controller-runtime with the same text the status condition shows.

## What this does not cover

<a id="h-14"></a>
### H-14 — A credential written into `spec.config` is stored in a ConfigMap

Live whenever `spec.config` carries a credential — a bridge's `remote_password`, for example, which
Mosquitto takes inline. `BuildConfigMap` writes the rendered file into `<name>-config` under the key
`mosquitto.conf`, so the credential is readable twice: with `get` on `mosquittoes` and with `get` on
`configmaps` in that namespace. A ConfigMap is not a Secret: an encryption-at-rest configuration of
the API server that covers Secrets only does not cover it, and roles that may read ConfigMaps but
not Secrets read it. What a cluster operator can do meanwhile: keep credentials out of
`spec.config`, and treat read access to `mosquittoes` and to ConfigMaps in a broker's namespace as
read access to whatever its `spec.config` holds.

### Encryption at rest

etcd encryption of the `Mosquitto` objects and ConfigMaps, and encryption of the broker's volume,
are the cluster's and the storage class's; the operator neither requires nor checks either.
