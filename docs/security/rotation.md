# How a change reaches a running broker, and what does not

Which changes to a `Mosquitto`, to its users or to the material they reference reach the broker
pods that are already running, by which mechanism, how fast, and which never do — a renewed TLS
certificate above all.
Where that material lives is [credentials.md](credentials.md); what is checked before a change is
stored is [validation.md](validation.md).

## The two hashes on the pod template

The StatefulSet's pod template carries two hash annotations, and `StatefulSetHasChanged` compares
them with the replica count and the operator's other keys on the object and the template
([`internal/builder/statefulset.go`](../../internal/builder/statefulset.go)):

- `mko.gtrfc.com/pod-spec-hash` (`AnnotationPodSpecHash`) — `hashOf(podSpec)`, the whole pod spec
  the operator built;
- `mko.gtrfc.com/config-hash` (`AnnotationConfigHash`) — `hashOf(GenerateMosquittoConf(m))`.

Mosquitto reads its configuration once at startup and a ConfigMap update restarts nothing, so the
config hash is what carries a configuration change into a roll. `hashOf` is a 32-bit FNV-1a digest
of the JSON encoding, and its own comment scopes it: "only used to detect change, never to prove
identity". It is not an integrity control, and nothing treats it as evidence about anything but the
operator's own desired object — whoever could rewrite the annotation already holds `update` on the
StatefulSet and could rewrite the pod template outright.

## What reaches running pods

| Change | Reaches running pods? | Mechanism |
|---|---|---|
| `spec.image`, `spec.resources`, `spec.antiAffinity` | Yes, by a roll | They are in the pod spec, so the pod-spec hash changes and the StatefulSet controller replaces the pods (`TestPodTemplateHashesChangeWithTheThingTheyDigest`) |
| `spec.config`, and anything else in the generated file | Yes, by a roll | The config hash digests the rendered `mosquitto.conf` (`TestIntegration_Reconcile_ConfigChangeReachesThePodTemplate`) |
| A `MosquittoUser` added, deleted or changed, or its credentials Secret changed — a rotated password | **Yes, without a restart** | `<broker>-auth` is re-rendered; the kubelet refreshes its mount, `reloader` copies it in and sends `SIGHUP`; a removed user and connections made with a changed password are dropped at that reload (observed on Kind: `TestE2E_Users_TheBrokerFollowsItsUsers`). [H-18](#h-18) is how long it takes |
| A new operator version | Yes, by a roll of **every** broker | `auth-init` and `reloader` run the operator's own image, part of the pod spec ([ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md) D6) |
| `spec.tls` switched on or off | Yes, by a roll | Both hashes change: the mounts and the listener move together |
| `spec.tls.secretName` pointing at a **different** Secret | Yes, by a roll | The name is part of the pod spec, so the pod-spec hash changes. Derived by reading `buildPodSpec` and `hashOf`; no test renames a Secret |
| `spec.replicas` | Pods are added or removed; the running ones are not rolled | Only the replica count moves; the template and both hashes stay (`TestReplicaChangeDoesNotRollThePods`) |
| **New bytes inside the referenced Secret** — a renewal | **No** | [H-3](#h-3) |
| `spec.storage` after creation | **No** | `volumeClaimTemplates` are immutable, and `reconcileStatefulSet` writes only `Spec.Replicas`, `Spec.Template` and the labels |

A roll is the StatefulSet controller's, one pod at a time; the brokers are independent processes
with no shared sessions, so every client of a replaced pod is disconnected and reconnects. Observed
on Kind for pod labels (`TestE2E_PodMetadata_ReachesAndLeavesThePods`).

## What this does not cover

<a id="h-3"></a>
### H-3 — A renewed certificate never reaches a running broker

Live, and dormant until the first renewal — at which point it is an availability failure, not an
attack; no principal is needed. The operator does not read the TLS Secret's data, so neither hash
has ever seen its bytes
([ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md) D6, D7). When
cert-manager renews, or somebody replaces the material by hand:

1. The Secret changes. The files under `/mosquitto/tls` are expected to follow it, because the mount
   uses no `subPath`; that half is kubelet behaviour and was not observed on a cluster here — only
   the absence of `subPath` was read.
2. The running broker keeps presenting the certificate it read at start. No event, no condition and
   no log line anywhere in this operator marks the moment.
3. Nothing converges until the pods restart.

The consequence is an outage on a timer, on exactly the clusters that automated issuance was meant
to protect: a long-lived pod outlives its own certificate. The same warning is written on
`MosquittoTLS.SecretName`, in the chart's
[`values.yaml`](../../deploy/helm/mosquitto-operator/values.yaml) and in the
[README](../../README.md), so a user meets it wherever they arrive.

What a cluster operator can do meanwhile: restart the pods after every renewal —
`kubectl rollout restart statefulset/<name>` — and watch certificate expiry from outside this
operator, because nothing in it will tell. Measured outside a cluster, the pinned broker image
re-reads `certfile` and `keyfile` on SIGHUP and keeps its existing connections, but a mismatched
certificate and key then fail every new handshake until a valid pair and a second SIGHUP arrive
([broker-behaviour.md](../developer/broker-behaviour.md), M12); sending that signal into a broker
pod by hand was not tried here, and this operator sends none.

### A changed `spec.storage` does not converge

A change to `spec.storage` on an existing resource is not written to the StatefulSet's claim
templates, and moving between an `emptyDir` and a claim changes the pod template without changing
the templates; what the API server answers to that update was not tested here. The StatefulSet has
to be recreated by hand, and its claims outlive it ([tenancy.md H-12](tenancy.md#h-12)).

<a id="h-18"></a>
### H-18 — A revoked credential keeps working for about a minute, and as long as the operator is down

Live, and the mechanism's cost
([ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md)
D8). A deleted `MosquittoUser`, a deleted credentials Secret or a rotated password reaches the
broker only when the kubelet refreshes the mounted `<broker>-auth`: 69 to 84 seconds after the
change on an idle Kind node with default kubelet settings
([broker-behaviour.md](../developer/broker-behaviour.md) M23), longer on a loaded node or with a
longer kubelet sync period — not measured. Until then the old credential logs in and its open
connections stay. While the operator is not running nothing re-renders `<broker>-auth` at all, so a
revocation waits for the operator. A user's `Ready=True` means rendered, not loaded. What a cluster
operator can do when a credential is compromised: delete the user or rotate the password, then
delete the broker pod (`kubectl delete pod <broker>-0`) — the restarted pod copies the current
`<broker>-auth` at start, at the cost of disconnecting every client.
