# Trust boundaries: who is trusted with what

Who acts on a cluster that runs this operator — the administrator who installs it, the operator
process, whoever writes a `Mosquitto` or a `MosquittoUser`, the broker pods, the MQTT clients and
the CI that builds the image — what each is trusted with, and where the boundaries between them are. What the operator's
grant permits verb by verb is [privilege-footprint.md](privilege-footprint.md); where the TLS
material and the broker's data live is [credentials.md](credentials.md); what keeps one broker or
namespace from another is [tenancy.md](tenancy.md); the pipeline and its credentials are
[ci-and-supply-chain.md](ci-and-supply-chain.md).

## Principals and what each is trusted with

| Principal | Identity | Scope | Trusted with |
|---|---|---|---|
| **Cluster administrator** | Whoever runs `helm install` or applies `kustomize build config/default` | Cluster | Creates the ClusterRole, the ClusterRoleBinding and the CRD — the CRD from the chart, or separately from `config/crd` through `make install`, because `kustomize build config/default` renders no CustomResourceDefinition (rendered 2026-10-05: six objects, none of them the CRD). Decides whether the operator's metrics port is open (`metrics.enabled`) and whether any NetworkPolicy exists at all — none ships here |
| **Operator manager** | A ServiceAccount bound by a **ClusterRoleBinding**: the chart's `fullname` — `mosquitto-operator` for the README's install command, which names the release `mosquitto-operator`, otherwise `<release>-mosquitto-operator` — or `mosquitto-operator-mosquitto-operator` in `mosquitto-operator-system` on the kustomize path | **Cluster-wide, every namespace** — for Secrets every namespace with `secretAccess.mode: all`, the listed ones with `namespaces` | `create`, `get`, `list`, `update`, `watch` on `configmaps`, `services`, `statefulsets` and `secrets`; `get`, `list`, `watch` on `mosquittoes` and `mosquittousers`; `update` on their status and on `mosquittoes/finalizers`. No `delete`, no `patch` ([privilege-footprint.md](privilege-footprint.md)). Reads the credentials Secrets the users name, writes `<broker>-auth` |
| **CR author** | Anyone with `create` or `update` on `mosquittoes.mko.gtrfc.com` in a namespace | That namespace | Chooses `spec.image` (any string), the tuning in `spec.config` (allowlisted directives only), the name of the Secret mounted as TLS material, `spec.replicas` (1–9), the storage size and class, the container resources, the pod labels and annotations. [H-2](#h-2) and [H-15](#h-15) are what that buys them |
| **User author** | Anyone with `create` or `update` on `mosquittousers.mko.gtrfc.com` in a namespace | That namespace | Adds a login to a broker of the namespace: names the broker, a Secret of the namespace whose username and password the operator reads and renders, and the topics. [H-15](#h-15) is what naming a Secret buys them |
| **Broker pods** | The namespace's `default` ServiceAccount — `buildPodSpec` sets no `ServiceAccountName` — with `AutomountServiceAccountToken: false` ([`internal/builder/statefulset.go`](../../internal/builder/statefulset.go)) | None: no token is mounted | Nothing through the API. The broker runs the image of `spec.image`; `auth-init` and `reloader` run the operator's own image and read the mounted `<broker>-auth` — hashes, never a plaintext — and signal the broker, which they may because they share its uid |
| **MQTT clients** | **A username and password of a `MosquittoUser`.** Anonymous clients are refused; the client ID is the username | Anything that can route to the broker — the ClusterIP Service `<name>` on 1883, or on 8883 under TLS, or a broker pod's IP directly | Publish and subscribe on the topics of their ACL; nothing under `$`. Without `spec.tls` the password crosses the network in plaintext ([tenancy.md H-6](tenancy.md#h-6)) |
| **CI** | GitHub Actions jobs, every one on `self-hosted` runners | The runner fleet; on non-fork runs also Docker Hub and this GitHub repository | `DOCKERHUB_PAT`, the GitHub App credentials `APP_CLIENT_ID`/`APP_PRIVATE_KEY` with the installation token minted from them, and the job `GITHUB_TOKEN` — which job holds which, and what bounds it, is [ci-and-supply-chain.md](ci-and-supply-chain.md) |

## The picture

```
        cluster scope                              namespace scope (any namespace)
  +------------------------------+        +-----------------------------------------+
  | ClusterRole                  |        | Mosquitto <name>       (the CR author)  |
  |   configmaps, services,      |        | MosquittoUser <user>   (the user author)|
  |   statefulsets, secrets(*)   |        |   +-- Secret <creds>   (user-owned,     |
  |     create/get/list/         |        |   |     read by the operator)           |
  |     update/watch             |        |   +-- Secret      <name>-auth (hashes)  |
  |   mosquittoes, mosquitto-    |        |   +-- ConfigMap   <name>-config         |
  |   users  get/list/watch      |        |   +-- Service     <name>-headless       |
  |   .../status      update     |        |   +-- Service     <name>  :1883|:8883   |
  |   .../finalizers  update     |        |   +-- StatefulSet <name> -> broker pods |
  +---------------+--------------+        +-----------------------------------------+
   (*) mode "all"; mode "namespaces":                      ^
       one Role per listed namespace                       | creates + owns
                  | ClusterRoleBinding                     | (controller ownerReference)
                  v                                        | never deletes, never patches
  +------------------------------+                         |
  | ServiceAccount <fullname>    |-------------------------+
  | operator Deployment          |   caches Secrets without their data,
  | (release namespace)          |   reads a credentials Secret with one get
  | :8080 metrics  :8081 health  |  both plain HTTP, no authentication filter
  +------------------------------+

  broker pod:  auth-init + reloader (operator image) --copy--> /mosquitto/auth --> broker
               <name>-auth mounted read-only              SIGHUP on change, same uid

  MQTT client --TCP 1883 (8883 under TLS)--> Service <name> --> broker pods
               username + password of a MosquittoUser, ACL per topic
```

## The two boundaries that matter

1. **Operator and workload.** The operator is cluster-wide and can create or overwrite a ConfigMap,
   a Service or a StatefulSet in **every** namespace. Replacing a StatefulSet's pod template is
   replacing the code that runs, so a compromised operator identity is a cluster-wide problem,
   bounded only by the verbs it does not hold and by what admission lets into a namespace
   ([privilege-footprint.md H-5](privilege-footprint.md#h-5)).
2. **CR author and broker.** The CR author picks the image the broker runs, so there is still no
   boundary between them worth the name; what changed is `spec.config`, which takes only
   allowlisted tuning directives and can no longer open a listener, load a plugin, bridge, or turn
   anonymous access on
   ([ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
   D15). RBAC on `mosquittoes` is the only control surface for the image ([H-2](#h-2)).
3. **Operator and Secrets.** To render users, the operator reads Secrets and writes one per broker,
   by default in every namespace ([privilege-footprint.md H-17](privilege-footprint.md#h-17)). Its
   cache holds no Secret data, but its identity can read any Secret it is granted.

## What does not exist in this tree

Named so that nothing on these pages is read as describing it: admission webhooks,
NetworkPolicies, PodDisruptionBudgets, ServiceMonitors, PrometheusRules, any per-instance
ServiceAccount, client certificates (`require_certificate`), roles or groups of users, and any
dynamic-security plugin. A broker metrics exporter is a recorded decision with no code behind it
([ADR 0002](../adr/0002-the-metrics-exporter-is-written-here.md)): `buildPodSpec` builds the broker
and the reloader, nothing else.

## What this does not cover

<a id="h-2"></a>
### H-2 — Whoever may write a `Mosquitto` picks its image

Live today, in every namespace where a principal holds `create` or `update` on
`mosquittoes.mko.gtrfc.com`. `spec.image` is any string — no registry allowlist, no digest
requirement, no pattern in the schema — and the image is the code that runs in the broker pod, with
the rendered credentials copied in and the TLS Secret mounted. An image of the author's choice can
log every password a client sends, accept any login, or ignore the ACLs. The operator's own
`auth-init` and `reloader` do not change that: they run next to the broker, not instead of it.

**Narrowed by the `spec.config` allowlist**
([ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
D15, `builder.ValidateSpecConfig`): `spec.config` takes 26 tuning directives and nothing else, so it
can no longer add a listener, override `listener_allow_anonymous`, load a plugin, bridge messages
out or redirect logs — a line that tries is refused by name and nothing is written
(`TestValidateSpecConfig`, `TestReconcile_ARefusedConfigWritesNothing`, and on Kind
`TestE2E_Users_TheBrokerFollowsItsUsers`). What the allowlist leaves the author is tuning — limits,
queues, keepalive, persistence intervals, log types — and `max_connections 0` or `max_qos 0` can
still degrade the broker for its users.

What a cluster operator can do: treat `create` and `update` on `mosquittoes.mko.gtrfc.com` as
authority over the code that handles a broker's credentials, grant it accordingly, and review
`spec.image` wherever changes reach the cluster — a GitOps review, or an admission policy of the
cluster's own; none ships here.

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

**The `MosquittoUser` half.** A user author names a credentials Secret, and the operator reads it
with its own grant and renders its `username` and `password` keys — or the keys the user names —
into the broker. That does not show the author the password: the operator writes only a `$7$` hash
into `<broker>-auth`, a Secret of the same namespace, readable only by whoever can read Secrets
there anyway. What it does give the author is a **login made of someone else's credentials**: any
Secret of the namespace with two string keys becomes a valid username and password on the broker,
usable by whoever knows them — typically the application that owns that Secret. With
`secretSecurity: true` the same label rule applies: an unlabelled Secret is refused with reason
`SecretNotConsumable` before its data is read (`TestReconcile_EveryUserReason`). At the default,
treat `create` on `mosquittousers` as the authority to turn any credential pair of the namespace
into a broker login.

<a id="h-16"></a>
### H-16 — The `Failed` condition tells a `Mosquitto` writer which objects exist

Live today, accepted by the owner. When a managed name is taken by an object the `Mosquitto` does
not control, `ensureOwned` refuses it with `<Kind> <namespace>/<name> exists and is not owned by
this Mosquitto`, and the reconcile copies that text into the `Ready` condition
([`internal/controller/mosquitto_controller.go`](../../internal/controller/mosquitto_controller.go)).
The operator reads with its cluster-wide grant, so a subject who may create a `Mosquitto` and read
its status, but may not list ConfigMaps, Secrets, Services or StatefulSets in the namespace, learns
whether an object of those kinds exists under a name derived from one they choose (`<name>`,
`<name>-headless`, `<name>-config`, and the Secret `<name>-auth`). Names only, never content. The message stays precise because
it is the administrator's one diagnostic for the refusal
([ADR 0009](../adr/0009-delete-only-through-owner-references.md) D5).

What a cluster operator can do: nothing in the operator closes it; where it matters, grant
`mosquittoes` only to subjects who may list those kinds in the namespace, which in most clusters
they already may.
