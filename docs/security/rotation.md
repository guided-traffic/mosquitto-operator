# How a change reaches a running broker, and what does not

Which changes to a `Mosquitto`, to its users or to the material they reference reach the broker
pods that are already running, by which mechanism, how fast, and which never do.
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
| **New bytes inside the referenced TLS Secret** — a renewal | **Yes, without a restart** | The kubelet refreshes the mount, `reloader` checks that `tls.crt` and `tls.key` form a pair (`crypto/tls.X509KeyPair`) and sends `SIGHUP`; new handshakes get the new certificate, open connections stay (observed on Kind: `TestE2E_TLS_ACertManagerRenewalIsReloaded`). An invalid pair is never signalled and the previous certificate stays served (`TestE2E_TLS_AMismatchedPairIsNeverLoaded`) — [H-19](#h-19). As slow as a credential change, [H-18](#h-18) |
| `spec.storage` after creation | **No** | `volumeClaimTemplates` are immutable, and `reconcileStatefulSet` writes only `Spec.Replicas`, `Spec.Template` and the labels |

A roll is the StatefulSet controller's, one pod at a time; the brokers are independent processes
with no shared sessions, so every client of a replaced pod is disconnected and reconnects. Observed
on Kind for pod labels (`TestE2E_PodMetadata_ReachesAndLeavesThePods`).

## What this does not cover

<a id="h-19"></a>
### H-19 — An invalid TLS pair holds back every reload, and a pod that restarts with it does not start

Live whenever the TLS Secret holds a certificate and a key that do not belong together — a hand
edit that replaced one of the two, or a tool that writes them in two updates. One `SIGHUP` reloads
the credentials and the certificate together, and an invalid pair loaded on a reload fails every
new handshake (M12 in [broker-behaviour.md](../developer/broker-behaviour.md)). So the reloader
checks the mounted pair before every signal and sends **none** while it is invalid
([ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md) D10,
`TestRound_AnInvalidPairBlocksEverySignal`). The broker keeps serving its previous certificate
and its open connections (`TestE2E_TLS_AMismatchedPairIsNeverLoaded`). Two costs remain:

1. **A credential change waits too.** A deleted user, a rotated password or a removed ACL is
   copied but not signalled until a valid pair is mounted — a revocation is held back for as long
   as the pair stays broken, on top of [H-18](#h-18).
2. **A pod that starts while the pair is invalid does not start.** The broker exits at startup on
   a mismatched pair (M24), so a pod rescheduled, evicted or rolled in that window crash-loops
   until the Secret is fixed.

The reloader logs `the TLS pair is not valid` once, in the `reloader` container of each broker
pod; nothing reaches the `Mosquitto`'s status, because the operator never reads the TLS Secret's
data. The check is the pair only: a certificate that matches its key but has expired, or names
other hosts, is loaded. What a cluster operator can do: write certificate and key in one update
(cert-manager does), and alert on that log line.

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
