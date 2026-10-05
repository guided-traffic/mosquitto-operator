# Where credentials and broker data live

Which credentials and which data the operator and its brokers handle on a cluster, the path each
one takes, and the places a credential can end up without anybody meaning it to. The credentials of
the build and release pipeline are [ci-and-supply-chain.md](ci-and-supply-chain.md); what reaches a
running broker after a change, a rotated certificate included, is [rotation.md](rotation.md).

## What the operator holds

Its own ServiceAccount token ([privilege-footprint.md H-5](privilege-footprint.md#h-5)) and, while
a pass runs, the users' passwords. With `secretAccess.mode: all` it may read and write every Secret
of the cluster, with `namespaces` those of the listed namespaces
([privilege-footprint.md H-17](privilege-footprint.md#h-17)).

- **The cache holds no Secret data.** Secrets are cached with `data`, `stringData`, the
  annotations — `kubectl apply` records a whole Secret in one of them — and the managed fields
  stripped (`controller.StripSecret` in
  [`internal/controller/watches.go`](../../internal/controller/watches.go), wired in
  [`cmd/main.go`](../../cmd/main.go)). Names, labels, owners and the type stay, which is what the
  watches and `--secret-security`'s label check read.
- **A password is read when its user is rendered**, with one uncached `get` of the user's
  credentials Secret, after the label check, and leaves the pass as a `$7$` hash
  (`readCredentials` in [`internal/controller/users.go`](../../internal/controller/users.go)). The
  plaintext is not logged and not written anywhere; it lives in the process's memory until the
  pass ends.
- **The TLS Secret's data is never read.** With `secretSecurity: true` its labels are, through a
  metadata-only `get`.

## User credentials

| Step | What happens | Read from |
|---|---|---|
| The Secret is created | By its owner — by hand, by SOPS through Flux, by whatever creates the client's Secret. **Never by this operator**: it generates no password | [ADR 0013](../adr/0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md) D2 |
| A `MosquittoUser` names it | `spec.credentialsSecret.name`, in the user's own namespace | [`api/v1/mosquittouser_types.go`](../../api/v1/mosquittouser_types.go) |
| The operator renders it | Username and password read, the password hashed as `$7$` PBKDF2-SHA512, 1000 iterations, 64-byte random salt; an existing hash is kept while the password still verifies against it | [`internal/auth/render.go`](../../internal/auth/render.go), [`hash.go`](../../internal/auth/hash.go) |
| `<broker>-auth` holds the result | Keys `passwd` (`username:hash` lines) and `acl`; **hashes only**; owned by the `Mosquitto` and collected with it. It lies in the same namespace as the Secrets it is rendered from, so whoever can read it could already read the plaintext | [ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md) D2 |
| The broker pod mounts it | As `auth-secret` at `/mosquitto/auth-secret`, mode `0440`, readable through the pod's group `1883`, in `auth-init` and `reloader` only | [`internal/builder/statefulset.go`](../../internal/builder/statefulset.go) |
| The broker reads a copy | `auth-init` (every start) and `reloader` (every change) copy both files into the `emptyDir` `auth` at `/mosquitto/auth`, `1883:1883` mode `0600`; the broker mounts it read-only | [`internal/reloader/`](../../internal/reloader) |

A pod compromise exposes the hashes in that `emptyDir` and the mount; at 1000 iterations they are
cheap to attack offline, which ADR 0014 D3 accepted because a compromised broker pod reads every
password in transit anyway. The plaintext reaches the broker only in the client's `CONNECT`.

## TLS material

| Step | What happens | Read from |
|---|---|---|
| The Secret is created | By hand (`kubectl create secret tls`) or by a cert-manager `Certificate` the administrator owns. **Never by this operator** — no cert-manager dependency in [`Chart.yaml`](../../deploy/helm/mosquitto-operator/Chart.yaml), no cert-manager module in [`go.mod`](../../go.mod), and `TestIntegration_TLS_DoesNotWaitForTheSecret` asserts the operator creates none | [ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md) D1–D3 |
| The pod references it | `buildPodSpec` adds a `corev1.SecretVolumeSource` with `SecretName: m.Spec.TLS.SecretName`, `DefaultMode` `0o644` and **no `Items` projection**; the Secret resolves in the resource's own namespace | [`internal/builder/statefulset.go`](../../internal/builder/statefulset.go) |
| The kubelet mounts it | At `TLSMountPath = "/mosquitto/tls"`, `ReadOnly: true`. Because there is no projection, **every key the Secret carries appears in that directory**, not only the two the configuration names — a cert-manager Secret's `ca.crt` among them | [`internal/builder/configmap.go`](../../internal/builder/configmap.go), [`internal/builder/statefulset.go`](../../internal/builder/statefulset.go) |
| The broker reads it | The generated block names `certfile /mosquitto/tls/tls.crt` and `keyfile /mosquitto/tls/tls.key` — `TLSCertKey` and `TLSKeyKey` — read when the process starts and again on every `SIGHUP` | [`internal/builder/configmap.go`](../../internal/builder/configmap.go) |
| The `reloader` reads it | The same volume, read-only at the same path, in the `reloader` sidecar only (`buildReloaderSidecar`; not in `auth-init`, not in `config-check`). It reads `tls.crt` and `tls.key` to check that they form a pair before it signals the broker, keeps only their SHA-256 digests between rounds, and writes, logs and sends nothing of them | [`internal/reloader/tls.go`](../../internal/reloader/tls.go) |

The private key therefore travels kubelet → volume → broker process, and the `reloader` of the
same pod reads it too; it never passes through the operator. The `reloader` runs the operator's
image as the broker's uid `1883` with no capability, so a second process with the key in its
memory is the cost of checking the pair before every reload
([ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md) D10). The mount
was observed on Kind: a renewed Secret reaches both containers
(`TestE2E_TLS_ACertManagerRenewalIsReloaded`).

## Broker data at rest

The generated file sets `persistence true` with `persistence_location /mosquitto/data/`, so the
broker writes its retained messages and session state into the `data` volume: a PersistentVolumeClaim
from the `data` template when `spec.storage` is set, an `emptyDir` otherwise. The operator encrypts
nothing; what protects that data at rest is the storage class's, and the claims outlive the
resource ([tenancy.md H-12](tenancy.md#h-12)).

## Where credentials can end up by accident

- **`spec.config` is copied into a ConfigMap** — [H-14](#h-14).
- **Not into status, and not into Events.** The reconciler writes phase, ready replicas, observed
  generation, the user count and the `Ready` and `Users` conditions into a `Mosquitto`'s `.status`,
  and a user's username — not a credential — and its `Ready` condition into the user's; a refusal
  of a username quotes it, which is how a password mistakenly put under the username key would show
  up there; the condition message on a failed pass is the
  error text — the operator's own names objects and, for an unparsable storage size, that value; an
  API server's refusal can quote the value it refused. It records **no Events at all**: no `EventRecorder`, `Recorder` or `Eventf` appears in
  `internal/`, `cmd/` or `api/`. The `events` rule that does exist belongs to client-go's
  leader-election `LeaseLock`
  ([`config/rbac/leader_election_role.yaml`](../../config/rbac/leader_election_role.yaml)).
- **Broker logs go to stdout.** The generated file sets `log_dest stdout` with `log_type` `error`,
  `warning`, `notice` and `information`, so broker output is whatever `kubectl logs` shows and
  inherits the cluster's log retention and readers. At that level the broker logs every connection
  with its source address, client id and username (`u'probe'`, observed on Kind), never a
  password. The reloader logs `credentials changed, copied` and `signalled the broker to reload`,
  never file content.
- **The operator's own log** carries resource and object names on every create and update; the
  reconciler's own log lines carry no spec content, and a failed pass's error is logged by
  controller-runtime with the same text the status condition shows.

## What this does not cover

<a id="h-14"></a>
### H-14 — Whatever is written into `spec.config` is stored in a ConfigMap

Narrowed: `spec.config` takes only the allowlisted tuning directives
([ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
D15), and none of them takes a credential — `password_file`, a bridge's `remote_password` and every
plugin option are refused. What is left is a comment: a `#` line passes the allowlist, and
`BuildConfigMap` writes the whole file into `<name>-config`, readable with `get` on `mosquittoes`
or on `configmaps` in that namespace, where a Secret-only encryption at rest does not reach. What a
cluster operator can do: keep credentials out of `spec.config` entirely; a client's credentials
belong in its own Secret, named by a `MosquittoUser`.

### Encryption at rest

etcd encryption of the `Mosquitto` objects and ConfigMaps, and encryption of the broker's volume,
are the cluster's and the storage class's; the operator neither requires nor checks either.
