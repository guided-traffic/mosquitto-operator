# Isolation between brokers, resources and namespaces

What keeps one `Mosquitto` from reaching another's objects, what a broker exposes to the network
around it, and what deleting a resource or the operator takes with it and leaves behind. Who may
write a `Mosquitto` and what that buys them is [trust-boundaries.md](trust-boundaries.md); the
operator's own reach across namespaces is [privilege-footprint.md](privilege-footprint.md).

**Read the second half before treating a namespace as a tenant boundary.** The objects the
operator writes stay inside their resource's namespace and never adopt somebody else's; the
brokers themselves accept anonymous clients from anywhere the network lets in.

## What holds

- **The operator writes only into the resource's own namespace.** Every builder sets
  `Namespace: m.Namespace` on the ConfigMap, both Services and the StatefulSet
  ([`internal/builder/`](../../internal/builder)), and `controllerutil.SetControllerReference`,
  which runs before every write, refuses an owner in another namespace (controller-runtime
  v0.24.1, `validateOwner`: "cross-namespace owner references are disallowed"). A `Mosquitto` has
  no field that names an object in another namespace; the TLS Secret is a `SecretVolumeSource`,
  which has no namespace and resolves in the pod's own.
- **Every managed object carries a controller ownerReference**, set before the write, on the
  ConfigMap, both Services and the StatefulSet. Deleting a `Mosquitto` therefore removes the
  workload through garbage collection, and the operator holds no `delete` verb to achieve it
  ([ADR 0009](../adr/0009-delete-only-through-owner-references.md) D1, D2). The collection itself
  is the kube-controller-manager's and was not observed here: envtest runs no garbage collector.
- **A pre-existing object holding a derived name is refused, never adopted.** `ensureOwned` uses
  `metav1.IsControlledBy`, which matches the controller reference **and its UID**; the refusal
  travels up as a reconcile failure — `phase: Failed`, `Ready=False`, reason `ReconcileFailed`,
  the error text as the message — and the pass stops before the next object
  ([`internal/controller/mosquitto_controller.go`](../../internal/controller/mosquitto_controller.go);
  [ADR 0009](../adr/0009-delete-only-through-owner-references.md) D5). The Services are the
  sharpest case: `spec.selector` of a live Service is mutable, so nothing at the API level would
  stop the operator from repointing somebody else's Service at these pods; the ownership check is
  that stop. `TestIntegration_Reconcile_RefusesAnObjectItDoesNotOwn` holds it against a real API
  server (passing against envtest on 2026-10-05): a foreign `<name>-config` keeps its content and
  no StatefulSet is created.
- **A resource that is being deleted gets no writes at all.** `Reconcile` returns before
  `reconcileResources` when `DeletionTimestamp` is set, so the operator never races the collection
  ([ADR 0009](../adr/0009-delete-only-through-owner-references.md) D6).
- **The broker pods are hardened to the restricted Pod Security Standard.** From `buildPodSpec`
  and `buildBrokerContainer` ([`internal/builder/statefulset.go`](../../internal/builder/statefulset.go)):
  `AutomountServiceAccountToken: false`; `RunAsNonRoot: true`; `RunAsUser`, `RunAsGroup` and
  `FSGroup` all `1883`; the `RuntimeDefault` seccomp profile at pod level; on the container
  `AllowPrivilegeEscalation: false`, `ReadOnlyRootFilesystem: true` and `Capabilities.Drop: [ALL]`.
  The image entrypoint is bypassed — `Command: /usr/sbin/mosquitto -c …` — because it chowns
  `/mosquitto` when it runs as root, which this pod never does.
  `TestBuildStatefulSet_SatisfiesRestrictedPodSecurityStandard` checks every control of the
  standard the builder could violate, for every shape the builder produces; admission into a real
  namespace labelled `pod-security.kubernetes.io/enforce=restricted` was not observed.
- **Anti-affinity is per resource.** `BuildPodAntiAffinity` selects on `common.SelectorLabels(m)`,
  which carries `app.kubernetes.io/instance: <name>`, so one `Mosquitto` never repels another's
  pods (`TestAntiAffinityRepelsOnlyTheSameResource`).

## What does not hold

- **Every broker is anonymous**, on every `Mosquitto` the API can express, TLS or not — [H-1](#h-1).
- **The CR author controls the broker's configuration and image**, including listeners on ports no
  Kubernetes object declares — [trust-boundaries.md H-2](trust-boundaries.md#h-2).
- **No NetworkPolicy is shipped**, for the brokers or for the operator, so the network boundary the
  anonymous posture leans on is not built here — [H-6](#h-6).
- **A namespace is not a boundary for the operator itself.** The binding is a ClusterRoleBinding,
  and `ensureOwned` bounds the reconcile path, not the grant: a compromised operator identity is
  not subject to its own guard — [privilege-footprint.md H-5](privilege-footprint.md#h-5).

## What deletion takes, and what it leaves

| Action | Removed | Left behind |
|---|---|---|
| Deleting a `Mosquitto` | Its ConfigMap, both Services and the StatefulSet with its pods, through the owner references | The PersistentVolumeClaims of `spec.storage` — [H-12](#h-12) |
| `helm uninstall` | The operator, its RBAC, **the CRD**, and with the CRD every `Mosquitto` of the cluster and every object they own | The PVCs — [H-11](#h-11) |
| `make uninstall` | The CRD and the RBAC of `config/rbac` — the Makefile's comment warns that removing the CRD deletes every `Mosquitto` with its workload | The PVCs |

## What this does not cover

<a id="h-1"></a>
### H-1 — Every broker accepts anonymous clients

Live today, on every `Mosquitto` the current API can express. `GenerateMosquittoConf` appends
`allow_anonymous true` on the plaintext and on the TLS branch, unconditionally, and the API
models no authentication field of any kind
([ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
D1). Anything that can open a TCP connection to the broker — through the Service `<name>`, the
headless Service or a pod IP — can publish and subscribe to every topic. The generated file says
so in its own comments, so `kubectl get configmap <name>-config -o yaml` is a complete answer.
Without `allow_anonymous true` the pinned Mosquitto 2.1 image refuses every client on a listener
with no authentication configured, which is why the line is there (measured against the pinned
image and recorded in ADR 0008's Status).

**TLS changes the transport, not the trust.** With `spec.tls` set the generated block emits
`listener 8883`, `certfile` and `keyfile`, and nothing else; `require_certificate` occurs nowhere
in the generator. TLS encrypts the connection and proves the broker's identity to clients that
check it; it authenticates **no client to the broker**
([ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md) D9). An
encrypted anonymous broker is still an anonymous broker.

What a cluster operator can do meanwhile: a NetworkPolicy of the cluster's own that admits only the
intended clients to the broker pods ([H-6](#h-6)); RBAC on who may create a `Mosquitto` at all;
and authentication configured through `spec.config` by whoever owns the resource — which is the
same authority [trust-boundaries.md H-2](trust-boundaries.md#h-2) describes, and which nothing in
the operator checks.

<a id="h-6"></a>
### H-6 — No NetworkPolicy ships, for the brokers or for the operator

Live today. The network is the only boundary the anonymous posture has ([H-1](#h-1)), and this
project builds none of it: no template in the chart and no manifest under `config/` is a
NetworkPolicy. The chart states the reason in
[`values.yaml`](../../deploy/helm/mosquitto-operator/values.yaml) and
[`templates/service.yaml`](../../deploy/helm/mosquitto-operator/templates/service.yaml): a policy
that fits one cluster's CNI and monitoring topology fits few others. Without a policy of the
cluster's, every pod of the cluster reaches every broker pod on every port its process listens
on, and reaches the operator's `:8080` and `:8081`
([privilege-footprint.md H-4](privilege-footprint.md#h-4)). What a cluster operator can do: write
the policy — ingress to the broker pods (selector `app.kubernetes.io/instance=<name>`,
`app.kubernetes.io/name=mosquitto`, `app.kubernetes.io/managed-by=mosquitto-operator`) from the
intended clients only, and ingress to the operator pod from the monitoring namespace only.

<a id="h-11"></a>
### H-11 — `helm uninstall` deletes the CRD, and with it every broker in the cluster

Live, and it needs no attacker — a cluster administrator running the obvious command is enough.
The CRD is a plain template,
[`templates/crd.yaml`](../../deploy/helm/mosquitto-operator/templates/crd.yaml), with no
`helm.sh/resource-policy: keep` annotation, so `helm uninstall` removes it; removing a CRD removes
every object of that kind, and through the owner references every StatefulSet, Service and
ConfigMap they own. The PVCs survive ([H-12](#h-12)). The chain after the CRD deletion is
Kubernetes behaviour and was not observed here. What a cluster administrator can do: treat
`helm uninstall` as deleting every broker of the cluster, and run it only when that is meant. This
repository offers no path that removes the operator and keeps the brokers, and none was tested.

<a id="h-12"></a>
### H-12 — Deleting a `Mosquitto` leaves its PVCs behind

Live, by design. `buildVolumeClaimTemplates` produces a template named `data`; the StatefulSet
controller creates the claims, this operator holds no `delete` verb on anything, and it sets no
`persistentVolumeClaimRetentionPolicy`. Deleting a `Mosquitto` therefore leaves the broker's
persistence on disk — retained messages and session state, written by `persistence true` under
`/mosquitto/data/` ([credentials.md](credentials.md#broker-data-at-rest)). Deliberate — the data is
not the operator's to destroy — but it is retained data nothing tracks, and a new `Mosquitto` of
the same name in the same namespace gets the same claim names back from the StatefulSet controller
and with them the old data (claim naming is the StatefulSet controller's `data-<name>-<ordinal>`;
not observed here). Without `spec.storage` the data directory is an `emptyDir` and dies with the
pod instead. What a cluster operator can do: delete the claims by hand when the broker's data is
meant to go.
