# Isolation between brokers, resources and namespaces

What keeps one `Mosquitto` from reaching another's objects, what a broker exposes to the network
around it, and what deleting a resource or the operator takes with it and leaves behind. Who may
write a `Mosquitto` and what that buys them is [trust-boundaries.md](trust-boundaries.md); the
operator's own reach across namespaces is [privilege-footprint.md](privilege-footprint.md).

**Read the second half before treating a namespace as a tenant boundary.** The objects the
operator writes stay inside their resource's namespace and never adopt somebody else's, a user and
everything it names share one namespace, and every broker requires a login — but the brokers are
reachable from anywhere the network lets in, and the operator itself reaches Secrets across
namespaces.

## What holds

- **The operator writes only into the resource's own namespace.** Every builder sets
  `Namespace: m.Namespace` on `<name>-auth`, the ConfigMap, both Services and the StatefulSet
  ([`internal/builder/`](../../internal/builder)), and `controllerutil.SetControllerReference`,
  which runs before every write, refuses an owner in another namespace (controller-runtime
  v0.24.1, `validateOwner`: "cross-namespace owner references are disallowed"). A `Mosquitto` has
  no field that names an object in another namespace; the TLS Secret is a `SecretVolumeSource`,
  which has no namespace and resolves in the pod's own.
- **A user, its broker, its Secret and its ACLs share one namespace.** No reference of the
  `MosquittoUser` API carries a namespace field — `brokerRef` and `credentialsSecret` name objects
  of the user's own namespace — so a reference across namespaces cannot be written
  ([ADR 0013](../adr/0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md)
  D1). The reconciler lists users with `client.InNamespace(m.Namespace)` and reads their Secrets
  by `types.NamespacedName{Namespace: u.Namespace}` ([`internal/controller/users.go`](../../internal/controller/users.go)).
- **Every broker requires a login.** The generated listener sets `listener_allow_anonymous false`,
  binds the `password-file` and `acl-file` plugins and `use_username_as_clientid true`; there is no
  field that turns anonymous access back on and `spec.config` cannot either
  ([ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
  D13–D15). Observed on Kind: an anonymous `mosquitto_pub` gets `Connection Refused: not
  authorised`, a user is refused outside its ACL, and a second user using another's client ID does
  not take over its session (`TestE2E_Users_TheBrokerFollowsItsUsers`).
- **`secretAccess.mode: namespaces` bounds the operator's Secret reach** to the listed namespaces:
  one Role each, no Secret rule in the ClusterRole, the Secret cache restricted to the same list,
  and a `Mosquitto` elsewhere refused with nothing written.
- **Every managed object carries a controller ownerReference**, set before the write, on
  `<name>-auth`, the ConfigMap, both Services and the StatefulSet. Deleting a `Mosquitto` therefore removes the
  workload through garbage collection, and the operator holds no `delete` verb to achieve it
  ([ADR 0009](../adr/0009-delete-only-through-owner-references.md) D1, D2). Observed on Kind: the
  E2E suite deletes a `Mosquitto` and waits for all five objects to be collected.
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
  and `containerSecurityContext` ([`internal/builder/statefulset.go`](../../internal/builder/statefulset.go)):
  `AutomountServiceAccountToken: false`; `RunAsNonRoot: true`; `RunAsUser`, `RunAsGroup` and
  `FSGroup` all `1883`; the `RuntimeDefault` seccomp profile at pod level; on **every** container —
  the broker, `reloader`, `auth-init`, `config-check` — `AllowPrivilegeEscalation: false`,
  `ReadOnlyRootFilesystem: true` and `Capabilities.Drop: [ALL]`. The pod shares its process
  namespace so the reloader can signal the broker; it can because it runs as the same uid, with no
  capability (M22). The image entrypoint is bypassed — `Command: /usr/sbin/mosquitto -c …` —
  because it chowns `/mosquitto` when it runs as root, which this pod never does.
  `TestIntegration_PodSecurity_RestrictedAdmitsEveryShape` has an API server's PodSecurity
  admission at `enforce=restricted` judge the pod of every shape the builder produces.
- **Anti-affinity is per resource.** `BuildPodAntiAffinity` selects on `common.SelectorLabels(m)`,
  which carries `app.kubernetes.io/instance: <name>`, so one `Mosquitto` never repels another's
  pods (`TestAntiAffinityRepelsOnlyTheSameResource`).

## What does not hold

- **The CR author picks the broker's image**, the code that handles every password a client sends —
  [trust-boundaries.md H-2](trust-boundaries.md#h-2).
- **No NetworkPolicy is shipped**, for the brokers or for the operator: every pod of the cluster
  can reach a broker, try passwords, and without TLS read them on the wire — [H-6](#h-6).
- **A namespace is not a boundary for the operator itself.** The binding is a ClusterRoleBinding,
  and `ensureOwned` bounds the reconcile path, not the grant: a compromised operator identity is
  not subject to its own guard — [privilege-footprint.md H-5](privilege-footprint.md#h-5).

## What deletion takes, and what it leaves

| Action | Removed | Left behind |
|---|---|---|
| Deleting a `Mosquitto` | `<name>-auth`, its ConfigMap, both Services and the StatefulSet with its pods, through the owner references | The PersistentVolumeClaims of `spec.storage` — [H-12](#h-12); its `MosquittoUser` objects, which report `BrokerNotFound`, and their Secrets |
| Deleting a `MosquittoUser` | Its login, at the broker's next reload, open connections included | Its credentials Secret, which is the user's |
| `helm uninstall` | The operator, its RBAC, **the CRDs**, and with them every `Mosquitto` and `MosquittoUser` of the cluster and every object they own | The PVCs and the credentials Secrets — [H-11](#h-11) |
| `make uninstall` | The CRD and the RBAC of `config/rbac` — the Makefile's comment warns that removing the CRD deletes every `Mosquitto` with its workload | The PVCs |

## What this does not cover

<a id="h-6"></a>
### H-6 — No NetworkPolicy ships, for the brokers or for the operator

Live today, by the owner's decision
([ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
D16). No template in the chart and no manifest under `config/` is a NetworkPolicy, so every pod of
the cluster reaches every broker pod on every port its process listens on, and the operator's
`:8080` and `:8081` ([privilege-footprint.md H-4](privilege-footprint.md#h-4)). What that means
now that every broker requires a login:

- **any workload can try passwords**, bounded by nothing but the login: no rate limit, no lockout.
  Each failed attempt costs the broker one PBKDF2 at 1000 iterations, so many attempts are also a
  way to load it ([ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md) D3);
- **without `spec.tls`, a client's username and password cross the pod network in plaintext**, to
  any workload that can observe that traffic;
- a user's ACL bounds what it reaches once logged in; the network bounds nothing.

What a cluster operator can do: write the policy — ingress to the broker pods (selector
`app.kubernetes.io/instance=<name>`, `app.kubernetes.io/managed-by=mosquitto-operator`) from the
intended clients only, an example of which is in the
[README fast start](../../README.md#-tldr-fast-start), step 4 — and serve MQTTS. It depends on the
CNI enforcing NetworkPolicy; none was tested here (the Kind run of that example applied the policy,
but its clients ran inside the broker pod, which the policy does not restrict).

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
