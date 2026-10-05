# Trust boundaries: who is trusted with what

Who acts on a cluster that runs this operator — the administrator who installs it, the operator
process, whoever writes a `Mosquitto`, the broker pods, the MQTT clients and the CI that builds the
image — what each is trusted with, and where the boundaries between them are. What the operator's
grant permits verb by verb is [privilege-footprint.md](privilege-footprint.md); where the TLS
material and the broker's data live is [credentials.md](credentials.md); what keeps one broker or
namespace from another is [tenancy.md](tenancy.md); the pipeline and its credentials are
[ci-and-supply-chain.md](ci-and-supply-chain.md).

## Principals and what each is trusted with

| Principal | Identity | Scope | Trusted with |
|---|---|---|---|
| **Cluster administrator** | Whoever runs `helm install` or applies `kustomize build config/default` | Cluster | Creates the ClusterRole, the ClusterRoleBinding and the CRD — the CRD from the chart, or separately from `config/crd` through `make install`, because `kustomize build config/default` renders no CustomResourceDefinition (rendered 2026-10-05: six objects, none of them the CRD). Decides whether the operator's metrics port is open (`metrics.enabled`) and whether any NetworkPolicy exists at all — none ships here |
| **Operator manager** | A ServiceAccount bound by a **ClusterRoleBinding**: the chart's `fullname` — `mosquitto-operator` for the README's install command, which names the release `mosquitto-operator`, otherwise `<release>-mosquitto-operator` — or `mosquitto-operator-mosquitto-operator` in `mosquitto-operator-system` on the kustomize path | **Cluster-wide, every namespace** | `create`, `get`, `list`, `update`, `watch` on `configmaps`, `services` and `statefulsets`; `get`, `list`, `watch` on `mosquittoes`; `update` on `mosquittoes/status` and `mosquittoes/finalizers`. No `delete`, no `patch`, no rule on `secrets` ([privilege-footprint.md](privilege-footprint.md)) |
| **CR author** | Anyone with `create` or `update` on `mosquittoes.mko.gtrfc.com` in a namespace | That namespace | Chooses `spec.image` (any string), the tail of the broker's `mosquitto.conf` through `spec.config`, the name of the Secret mounted as TLS material, `spec.replicas` (1–9), the storage size and class, the container resources. [H-2](#h-2) is what that buys them |
| **Broker pods** | The namespace's `default` ServiceAccount — `buildPodSpec` sets no `ServiceAccountName` — with `AutomountServiceAccountToken: false` ([`internal/builder/statefulset.go`](../../internal/builder/statefulset.go)) | None: no token is mounted | Nothing. The container command is `/usr/sbin/mosquitto -c /mosquitto/config/mosquitto.conf`; no code of this repository runs in that pod, and it makes no Kubernetes API call |
| **MQTT clients** | **None. There is no client identity.** | Anything that can route to the broker — the ClusterIP Service `<name>` on 1883, or on 8883 under TLS, or a broker pod's IP directly | Publish and subscribe on every topic. `GenerateMosquittoConf` appends `allow_anonymous true` unconditionally and renders no `require_certificate` ([`internal/builder/configmap.go`](../../internal/builder/configmap.go); [tenancy.md H-1](tenancy.md#h-1)) |
| **CI** | GitHub Actions jobs, every one on `self-hosted` runners | The runner fleet; on non-fork runs also Docker Hub and this GitHub repository | `DOCKERHUB_PAT`, the GitHub App credentials `APP_CLIENT_ID`/`APP_PRIVATE_KEY` with the installation token minted from them, and the job `GITHUB_TOKEN` — which job holds which, and what bounds it, is [ci-and-supply-chain.md](ci-and-supply-chain.md) |

## The picture

```
        cluster scope                              namespace scope (any namespace)
  +------------------------------+        +-----------------------------------------+
  | ClusterRole                  |        | Mosquitto <name>        (the CR author  |
  |   configmaps, services,      |        |                          writes this)   |
  |   statefulsets               |        |   +-- ConfigMap   <name>-config         |
  |     create/get/list/         |        |   |     mosquitto.conf, spec.config     |
  |     update/watch             |        |   |     appended verbatim               |
  |   mosquittoes get/list/watch |        |   +-- Service     <name>-headless       |
  |   .../status      update     |        |   +-- Service     <name>  :1883|:8883   |
  |   .../finalizers  update     |        |   +-- StatefulSet <name> -> broker pods |
  +---------------+--------------+        +-----------------------------------------+
                  | ClusterRoleBinding                      ^
                  v                                         | creates + owns
  +------------------------------+                          | (controller ownerReference)
  | ServiceAccount <fullname>    |--------------------------+ never deletes,
  | operator Deployment          |                            never patches,
  | (release namespace)          |                            no rule on secrets
  | :8080 metrics  :8081 health  |  both plain HTTP, no authentication filter
  +------------------------------+

  TLS Secret (user-owned)  --kubelet mounts it read-only-->  broker pod
  named by spec.tls.secretName   at /mosquitto/tls             the operator
                                                               never reads it

  MQTT client --TCP 1883 (8883 under TLS)--> Service <name> --> broker pods
                                    no client authentication of any kind
```

## The two boundaries that matter

1. **Operator and workload.** The operator is cluster-wide and can create or overwrite a ConfigMap,
   a Service or a StatefulSet in **every** namespace. Replacing a StatefulSet's pod template is
   replacing the code that runs, so a compromised operator identity is a cluster-wide problem,
   bounded only by the verbs it does not hold and by what admission lets into a namespace
   ([privilege-footprint.md H-5](privilege-footprint.md#h-5)).
2. **CR author and broker.** There is no boundary between them worth the name. `spec.config` is
   appended to the generated file verbatim and the broker reads it last, so whoever may write a
   `Mosquitto` in a namespace writes that broker's configuration and picks the image it runs
   ([ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
   D6, D7, D10). RBAC on `mosquittoes` is the only control surface for it, and there is no second
   one ([H-2](#h-2)).

## What does not exist in this tree

Named so that nothing on these pages is read as describing it: admission webhooks,
NetworkPolicies, PodDisruptionBudgets, ServiceMonitors, PrometheusRules, any per-instance
ServiceAccount, Role or RoleBinding, and any client authentication or ACL modelled by the API. A
broker metrics exporter sidecar is a recorded decision with no code behind it
([ADR 0002](../adr/0002-the-metrics-exporter-is-written-here.md)): `cmd/exporter` does not exist and
`buildPodSpec` builds exactly one container.

## What this does not cover

<a id="h-2"></a>
### H-2 — Whoever may write a `Mosquitto` picks its image and writes its whole configuration

Live today, in every namespace where a principal holds `create` or `update` on
`mosquittoes.mko.gtrfc.com`. `GenerateMosquittoConf` appends `spec.config` after everything it
generates; the only transformations are `strings.TrimSpace` for the emptiness check and
`strings.TrimRight(…, "\n")` on the content
([`internal/builder/configmap.go`](../../internal/builder/configmap.go)). So the CR author can:

- **override a generated global option**, `allow_anonymous` included — the order is pinned by
  `TestGenerateMosquittoConf_SpecConfigIsAppendedVerbatim`; what the broker answers to the repeated
  option was not measured here;
- **add listeners** that no container port and no Service port declares. A `spec.config` carrying
  `listener 1883` reopens plaintext on a broker whose `spec.tls` moved the generated listener to
  8883: "enabling TLS closes the plaintext port" holds for the generated block only, as the doc
  comment on `BrokerPort` says ([ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
  D8). A `containerPort` is documentation, not a firewall — any pod that can route to the broker
  pod's IP reaches every port the process listens on — and nothing detects this case;
- **bridge** messages to an external broker, and **redirect logs**.

`spec.image` is any string — no registry allowlist, no digest requirement, no pattern in the
schema — and the image is the code that runs in the broker pod. Nothing in this repository reports
any of it except the CR itself and the rendered ConfigMap `<name>-config`.

What a cluster operator can do meanwhile: treat `create` and `update` on
`mosquittoes.mko.gtrfc.com` as authority over a broker's code and configuration, grant it
accordingly, and review `spec.image` and `spec.config` wherever changes reach the cluster — a
GitOps review, or an admission policy of the cluster's own; none ships here.

<a id="h-15"></a>
### H-15 — Whoever may write a `Mosquitto` can read every Secret of its namespace

Live at the default `secretSecurity: false`, in every namespace where a principal holds `create`
or `update` on `mosquittoes.mko.gtrfc.com`, and it holds even when that principal may not `get`
Secrets or `create` pods there. `spec.tls.secretName` may name **any** Secret of the resource's namespace:
nothing checks its type or its keys, and `buildPodSpec` mounts it whole, with no `Items`
projection, at `/mosquitto/tls`
([`internal/builder/statefulset.go`](../../internal/builder/statefulset.go)). `spec.image` is any
image (H-2), and the broker container runs whatever that image puts at `/usr/sbin/mosquitto`. The
StatefulSet is written by the operator with its own cluster-wide grant, so the CR author borrows
the operator's authority to run code of their choice with a Secret of their choice mounted. A
legacy ServiceAccount token Secret in the namespace turns the read into acting as that
ServiceAccount. Derived from the code; not run on a cluster.

The principal and the adversary: a subject granted `mosquittoes` more narrowly than Secrets —
for example a team allowed to manage its broker but not to read the namespace's credentials.
Where the same subjects already read Secrets in the namespace, nothing new is exposed.

**The mitigation is the install-time switch `secretSecurity`** (chart value, `--secret-security`
flag, the kustomize component `config/components/secret-security`;
[ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md)
D10). With `true` the operator reads the named Secret's labels through a metadata-only `get` before
it writes anything, and a Secret without `mko.gtrfc.com/consumable=true` — the label whose
writer can already write the Secret, so its owner's consent — is refused: the resource is `Failed`
with reason `SecretNotConsumable`, nothing is written, and no new pod mounts it
(`TestReconcile_SecretSecurity`). The default stays `false`, the owner's choice against the
recommendation; at that default, treat `create` and `update` on `mosquittoes.mko.gtrfc.com` in a
namespace as equivalent to reading every Secret of that namespace, and grant it only to subjects
who may do that already. Either way: keep credentials a broker owner must not see in another
namespace, and remove legacy ServiceAccount token Secrets the namespace does not need.

With `true` two things remain: a `Mosquitto` author can still name any **labelled** Secret of the
namespace, so the label states consent to every broker of the namespace, not to one; and a label
removed later stops the next pass, not the pods that already run with the Secret mounted.

<a id="h-16"></a>
### H-16 — The `Failed` condition tells a `Mosquitto` writer which objects exist

Live today, accepted by the owner. When a managed name is taken by an object the `Mosquitto` does
not control, `ensureOwned` refuses it with `<Kind> <namespace>/<name> exists and is not owned by
this Mosquitto`, and the reconcile copies that text into the `Ready` condition
([`internal/controller/mosquitto_controller.go`](../../internal/controller/mosquitto_controller.go)).
The operator reads with its cluster-wide grant, so a subject who may create a `Mosquitto` and read
its status, but may not list ConfigMaps, Services or StatefulSets in the namespace, learns whether
an object of those kinds exists under a name derived from one they choose (`<name>`,
`<name>-headless`, `<name>-config`). Names only, never content. The message stays precise because
it is the administrator's one diagnostic for the refusal
([ADR 0009](../adr/0009-delete-only-through-owner-references.md) D5).

What a cluster operator can do: nothing in the operator closes it; where it matters, grant
`mosquittoes` only to subjects who may list those kinds in the namespace, which in most clusters
they already may.
