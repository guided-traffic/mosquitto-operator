# Runtime behaviour

This page covers what the operator process does from start to stop, what one reconcile pass writes
and what it leaves alone, which changes reach the running brokers and how, what the status says,
and what the operator answers and logs. Every field and flag named here is listed in
[README.md, Full reference](../../README.md#-full-reference). Nothing on this page has been
observed on a real cluster ([why](README.md)).

## At start

The container runs `./manager` ([`cmd/main.go`](../../cmd/main.go)). In order:

1. **Flags and logging.** The flags are parsed and the logger is set up in zap development mode
   ([logs](#logs)). The first line is `starting mosquitto-operator`, with the `version`, `commit`
   and `buildTime` the image was built with. A local build without build arguments shows `dev` and
   `unknown`.
2. **The manager is created** with the ServiceAccount's in-cluster credentials. If none can be
   found, controller-runtime logs `unable to get kubeconfig` and the process exits with code 1. If
   the manager cannot be created, the log says `unable to start manager` and the process exits with
   code 1.
3. **The controller is registered** under the name `mosquitto`. It watches `Mosquitto` resources,
   reacting only to changes of their `spec` (`GenerationChangedPredicate`), and it watches the
   StatefulSets, ConfigMaps and Services that a `Mosquitto` controls. If this fails, the log says
   `unable to create controller` and the process exits with code 1.
4. **Health checks.** `/healthz` and `/readyz` are registered ([ports and probes](#the-operators-ports-and-probes)).
5. **`starting manager`.** Several things now start on every replica, leader or not:
   - the metrics server on `--metrics-bind-address`, unless that is `0` (controller-runtime logs
     `Serving metrics server` with the address and `secure: false`);
   - the probe server on `--health-probe-bind-address`;
   - the cache. It lists and then watches every `Mosquitto`, ConfigMap, Service and StatefulSet in
     the cluster, in every namespace and not only the objects it manages. Its memory grows with the
     cluster's count of those objects, and nobody has measured it.
6. **Leader election**, with `--leader-elect`. The process waits until it holds the Lease
   `mosquitto-operator.mko.gtrfc.com` in its own namespace. Only the holder runs the controller
   ([installation.md, leader election](installation.md#leader-election-on-the-two-paths)).
7. **The controller starts** (`Starting Controller`, `Starting workers`), and every `Mosquitto` the
   cache holds gets a pass, up to `--max-concurrent-reconciles` of them at a time. Passes for one
   resource never run in parallel.

So every operator restart is a pass over every broker in the cluster. For a broker whose rendering
has not changed, that pass writes nothing: the comparisons below find no drift and the status is
unchanged. For a broker whose rendering the new version changes, the pass is a roll
([installation.md, upgrade](installation.md#upgrade)). If the manager later stops with an error,
the log says `problem running manager` and the process exits with code 1.

## One reconcile pass

A pass handles one `Mosquitto`
([`internal/controller/mosquitto_controller.go`](../../internal/controller/mosquitto_controller.go)):

- **The resource is gone:** nothing happens. The log says `Mosquitto resource not found, probably deleted`.
- **The resource is being deleted:** nothing is written, not even the status. The log says
  `Mosquitto resource is being deleted, skipping reconciliation`. The garbage collector removes
  the owned objects through their owner references
  ([ADR 0009](../adr/0009-delete-only-through-owner-references.md)).
- **Otherwise the four objects are written in order:** the ConfigMap, the headless Service, the
  client Service, then the StatefulSet. For each one, the pass does one of three things:
  - creates it if it is missing (log: `Creating <Kind>` with its name);
  - refuses it if this `Mosquitto` does not control it ([below](#an-object-the-operator-refuses));
  - updates it if it has drifted (log: `Updating <Kind>`).
- **Then the status** is recomputed from the live StatefulSet and written only if it changed
  ([status](#status)).

**A failure stops the pass.** If any write fails, the objects after it in the order are not
written. An integration test asserts this: no StatefulSet exists after a refused ConfigMap. The
resource is set to `Failed` with the error as its message, and the error goes back to the work
queue. The queue retries with controller-runtime's default backoff, which this operator does not
change: exponential per resource, from 5 ms up to 1000 s between attempts.

**Concurrency.** `--max-concurrent-reconciles` (chart value `maxConcurrentReconciles`) sets how
many *different* resources can be in a pass at once. It defaults to 4, not to controller-runtime's
1, so that one slow resource does not hold up every other `Mosquitto` in the cluster.

### What a pass corrects, and what it leaves alone

| Object | Compared: a difference triggers a write | When the operator writes, it replaces | Never written after creation |
|---|---|---|---|
| ConfigMap `<name>-config` | the data, the operator's own labels | the data and the whole label set | annotations |
| Both Services | the ports, the selector, the operator's own labels | the ports, the selector and the whole label set | the type, annotations, every other spec field |
| StatefulSet | the replica count, the operator's own labels on the object and on the pod template, the annotations `mko.gtrfc.com/pod-spec-hash` and `mko.gtrfc.com/config-hash` | the replica count, the whole pod template and the whole label set | annotations on the object; the claim template |

Labels that others add are not compared, so they never trigger a write. But when the operator
writes the object for another reason, it assigns its own label set, and labels added by others are
gone. The same happens to annotations that others add to the pod template whenever the operator
writes a new template.

Three consequences:

- **Edit `spec.config`, never the ConfigMap.** A hand edit of `<name>-config` triggers a pass,
  because the operator watches the ConfigMaps it controls, and that pass writes the generated file
  back. The edit restarts nothing in the meantime: the config hash is computed from the generated
  file, not from the ConfigMap.
- **A hand edit of the StatefulSet's pod template survives.** If you change the image or a limit
  directly on the StatefulSet, the hashes stay the same and the operator does not see a
  difference. The StatefulSet controller rolls the pods onto your edit. It stays until a `spec`
  change moves one of the two hashes; the operator then writes its whole pod template over it. A
  unit test asserts that a defaulted pod field the operator never writes is not drift
  (`TestStatefulSetHasChanged` in
  [`internal/builder/statefulset_test.go`](../../internal/builder/statefulset_test.go)).
- **`kubectl rollout restart` survives too.** The annotation it adds to the pod template is not
  compared, so the operator does not undo the restart (same test). The annotation disappears the
  next time the operator writes a template, and that write rolls the pods anyway.

### An object the operator refuses

Every managed name is derived from the `Mosquitto`'s name, so a ConfigMap, Service or StatefulSet
with that name can already exist. The operator writes such an object only if its controller
reference points at this exact `Mosquitto`, UID included; a label is not enough. Otherwise the pass
fails with a message that names the object:

```
ConfigMap messaging/broker-config exists and is not owned by this Mosquitto
```

The foreign object stays untouched. To resolve it, rename or remove the foreign object, or give the
`Mosquitto` another name. **The operator gets no event when the foreign object goes away**, because
only objects that a `Mosquitto` controls wake it. So the resource recovers at its next backoff
retry, which after a long series of failures can be many minutes away. Two things trigger a pass
immediately: a change to the resource's `spec`, or a restart of the operator.

## Which changes restart the broker pods

Mosquitto reads its configuration and its certificate once, at startup, and a ConfigMap update
restarts nothing. What carries a change into the running brokers is the pod template: the
StatefulSet controller replaces the pods whenever the template changes. The operator stamps two
hashes onto the template. `mko.gtrfc.com/pod-spec-hash` digests the whole pod spec it builds, and
`mko.gtrfc.com/config-hash` digests the generated `mosquitto.conf`.

| Change | What the operator writes | Broker pods |
|---|---|---|
| `spec.replicas` | the StatefulSet's replica count | Not restarted. Pods are added, or removed from the highest ordinal down. The claims of removed pods stay |
| `spec.config` | the ConfigMap, and a new config hash | Rolled |
| `spec.image` | the pod template (the container image, and the `app.kubernetes.io/version` label) | Rolled |
| `spec.resources` | the pod template | Rolled |
| `spec.antiAffinity` | the pod template | Rolled |
| `spec.tls` added or removed | the ConfigMap (the listener), the pod template (the Secret volume, the mount, the port, the probes) and the port of both Services | Rolled. Clients have to change their port (`1883` ↔ `8883`) |
| `spec.tls.secretName` pointing at another Secret | the pod template (the volume) | Rolled |
| New content in the referenced Secret | nothing | Not restarted ([a renewed certificate](#a-renewed-certificate)) |
| `spec.storage.size` or `.storageClassName` on a broker that has storage | nothing | Not restarted. The change never converges ([below](#changing-specstorage-on-an-existing-broker)) |
| `spec.storage` added or removed | the pod template only (the `emptyDir` appears or disappears), never the claim template | Rolled onto a template that no longer matches the claim template ([below](#changing-specstorage-on-an-existing-broker)) |
| A new operator version | whatever it renders differently | Rolled where the rendering differs ([installation.md, upgrade](installation.md#upgrade)) |

The image, config, TLS, anti-affinity and storage rows are asserted by
`TestPodTemplateHashesChangeWithTheThingTheyDigest`, and the replica row by
`TestReplicaChangeDoesNotRollThePods`
([`internal/builder/statefulset_test.go`](../../internal/builder/statefulset_test.go)). The
integration test `TestIntegration_Reconcile_ConfigChangeReachesThePodTemplate` checks the config
path against a real API server. The `resources` and `secretName` rows are derived from the pod
spec being hashed as a whole; no test pins them.

**How a roll proceeds.** The operator sets neither an update strategy nor a pod management policy
on the StatefulSet, so Kubernetes' defaults apply. Pods are replaced one at a time, starting at the
highest ordinal, and each next pod waits until the one before it is Ready. Ready means its TCP
probe passes. Each replaced pod drops its clients, which reconnect through the Service to whichever
pod answers. The replaced pod's sessions and retained messages are lost with an `emptyDir`, and
come back from its claim with `spec.storage`.

**The ConfigMap changes before the pods do.** The pass writes the ConfigMap ahead of the
StatefulSet, and the kubelet refreshes a mounted ConfigMap inside running pods. So a broker
container that restarts, in a pod the roll has not reached yet, starts with the new file in its old
pod. After a TLS switch, that means a file naming `/mosquitto/tls/tls.crt` in a pod with no TLS
mount. This is Kubernetes behaviour, not observed here. The roll replaces that pod anyway.

**Scheduling and node maintenance.** The operator ships no PodDisruptionBudget, so a node drain
evicts broker pods with nothing holding one back. With `antiAffinity: off`, the default, several
replicas can share one node. With `hard`, replicas beyond the number of schedulable nodes stay
`Pending`, and the resource stays `Progressing`.

### Changing `spec.storage` on an existing broker

The claim template of a StatefulSet is immutable. The operator writes it when it creates the
StatefulSet and never afterwards. That means:

- a new `size` or `storageClassName` is never written;
- adding or removing `spec.storage` changes the pod template but not the claim template.

What the StatefulSet controller does with a `data` mount that then matches neither a volume nor a
claim template was not observed here. Treat any `spec.storage` change on an existing broker as the
following manual step:

```bash
kubectl -n messaging delete statefulset broker        # example names; your credentials, not the operator's
kubectl -n messaging get statefulset broker -o jsonpath='{.spec.volumeClaimTemplates}'   # recreated by the operator
```

The operator has no `delete` permission, so the deletion is yours. The StatefulSet's deletion
wakes the operator, because the operator controls it, and the pass creates it again from the
current `spec`, claim template included. By default, deleting the StatefulSet also deletes its
pods, so the brokers are down until the new pods are Ready.

**Existing claims are reused as they are.** Claims named `data-<name>-<ordinal>` survive the
deletion, and the StatefulSet controller creates only the claims that do not exist yet
(Kubernetes behaviour, not exercised by any test here). A new size or class therefore reaches only
new ordinals. To change an existing claim, resize the claim itself where its storage class allows
it, or delete the claim and lose its data. Removing `spec.storage` this way leaves the old claims
in place, unused.

## Status

The phase table and its reasons are in [README.md, `status`](../../README.md#status). What they
mean in practice:

**Ready counts ready pods, not updated pods.** The phase compares the StatefulSet's
`readyReplicas` with `spec.replicas` and looks at nothing else. During a roll, the resource goes
from `Ready` to `Progressing` while a pod restarts (to `Pending` with a single replica), and back
again. `observedGeneration` is the
generation the operator wrote the objects for, not the generation the brokers run. To see whether
every pod runs the current template:

```bash
kubectl -n messaging rollout status statefulset/broker     # example names
```

**Ready is a TCP statement.** Both broker probes connect over TCP to the listener port. Readiness
starts after 5 s and checks every 5 s; liveness starts after 15 s and checks every 10 s
([`internal/builder/statefulset.go`](../../internal/builder/statefulset.go)). A broker that accepts
connections and refuses every MQTT `CONNECT` is Ready
([ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)).
Proving an MQTT session takes a publish and a subscribe, as in
[README.md, TL;DR fast start](../../README.md#-tldr-fast-start).

**`Pending` and `Progressing` say how many pods are ready, not why the rest are not.** Typical
causes, none of which the operator sees:

- an image that cannot be pulled;
- a missing TLS Secret: the pod waits on its volume mount;
- a `spec.config` the broker refuses: `CrashLoopBackOff`;
- an admission policy that rejects the pod: the StatefulSet exists and creates no pod. The broker
  pod satisfies the restricted Pod Security Standard
  ([installation.md](installation.md#the-namespace)), so this comes from a policy beyond it;
- no node left for `antiAffinity: hard`;
- a claim that cannot bind.

The reconciler emits no Events, so look at the StatefulSet and its pods:

```bash
kubectl -n messaging describe statefulset broker                          # example names
kubectl -n messaging get pods -l app.kubernetes.io/instance=broker
kubectl -n messaging logs broker-0
```

**`Failed` describes the operator, not the brokers.** It means a pass could not write one of the
four objects. Pods that were already running keep running on what they had. The causes:

- an object the operator refuses ([above](#an-object-the-operator-refuses));
- a `spec.storage.size` that does not parse as a quantity (`parsing spec.storage.size …`);
- any write the API server rejects, such as a `403` from RBAC that falls short, or a conflict with
  another writer.

The condition message carries the error:

```bash
kubectl -n messaging get mq broker -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}'
```

`Failed` stays until a later pass succeeds ([one reconcile pass](#one-reconcile-pass)). It raises
no Event. The signals are this status, the operator's `Reconciler error` log line, and the counter
`controller_runtime_reconcile_errors_total{controller="mosquitto"}` on the metrics port.

## The operator's ports and probes

**`:8080`, `/metrics`.** This is controller-runtime's own registry: reconcile counts and errors per
controller (`controller_runtime_reconcile_total`, `controller_runtime_reconcile_errors_total`,
both with `controller="mosquitto"`), plus the work queue, the API client and the Go runtime. The
operator registers no metric of its own, and none of the series names a `Mosquitto`. There is no
broker metrics exporter ([ADR 0002](../adr/0002-the-metrics-exporter-is-written-here.md), nothing
of it implemented).

**Security note:** the endpoint is plain HTTP with no authentication. Anything that can route to
the operator pod can read it, whether or not the chart's metrics Service exists. On the Helm path,
`metrics.enabled: false` closes the port. On the kustomize path the port is always open; to close
it, add `--metrics-bind-address=0` to the container's arguments in
[`config/manager/manager.yaml`](../../config/manager/manager.yaml). Neither path ships a
NetworkPolicy, so restricting who reaches the port is up to the cluster administrator
([trust-boundaries.md](../security/trust-boundaries.md)).

**`:8081`, `/healthz` and `/readyz`.** Both checks are `healthz.Ping`. They answer `200` as soon as
the probe server runs, and say nothing about whether the API server is reachable, whether the
cache has synced, or whether this replica holds the Lease. A standby replica is Ready. So is a pod
that never acquires the Lease, even though it reconciles nothing. A `Mosquitto` reaching
`PHASE=Ready` is the proof that the operator works. The probe timings differ between the two paths
([installation.md](installation.md#what-each-path-installs)).

**Shutdown.** On `SIGTERM` the manager stops. The pod's termination grace period is 10 seconds on
both paths. With leader election on, the stopping leader keeps the Lease until it expires, so a
successor starts reconciling up to 15 seconds later.

## When the operator is not running

The brokers keep running. They are ordinary StatefulSets that need nothing from the operator: no
sidecar, and no API call from the broker pod, which mounts no ServiceAccount token. While the
operator is down:

- nothing reconciles;
- `spec` changes wait;
- the status keeps saying what the last pass saw;
- a deleted `Mosquitto` is still collected, because the garbage collector works without the
  operator and no finalizer holds it.

When the operator comes back, every `Mosquitto` gets a pass ([at start](#at-start)).

To pause the operator on purpose, scale its Deployment to zero:

```bash
kubectl -n mosquitto-operator-system scale deploy/mosquitto-operator --replicas=0   # example: Helm release "mosquitto-operator"; kustomize: deploy/mosquitto-operator-mosquitto-operator
```

On the Helm path, the next `helm upgrade` sets the replica count back to `replicaCount`.

## Logs

**The operator** logs to stderr in zap development mode: the console encoder, `debug` level, and a
stack trace from `warn` up. That is what `bindZapFlags` starts from, and neither install path
passes a zap flag. The flags exist (`--zap-log-level`, `--zap-encoder`, `--zap-stacktrace-level`,
`--zap-time-encoding`, `--zap-devel`), and `--zap-encoder=json` switches the output to JSON. The
chart has no value for extra arguments, so on the Helm path the log format and level cannot be
changed without changing the template. On the kustomize path, add the flags to the arguments in
`manager.yaml`.

| Message | Level | Meaning |
|---|---|---|
| `starting mosquitto-operator` | info | The process started; carries `version`, `commit`, `buildTime` |
| `starting manager` | info | The servers and the cache start next, then leader election if enabled, then the controller |
| `Serving metrics server` | info | controller-runtime; carries the bind address and `secure: false` |
| `Starting Controller`, `Starting workers` | info | controller-runtime; the controller runs. With leader election, only on the leader. `worker count` is `--max-concurrent-reconciles` |
| `Creating ConfigMap`, `Creating Service`, `Creating StatefulSet`, `Updating …` | info | A pass wrote that object; carries `name` |
| `Mosquitto resource not found, probably deleted` | info | A pass for a resource that is gone |
| `Mosquitto resource is being deleted, skipping reconciliation` | info | A pass for a resource that is being deleted; nothing written |
| `Reconciler error` | error | controller-runtime; a pass failed with the error that the status also carries. It is retried with backoff |
| `Failed to record the reconcile failure on the resource` | error | After a failed pass, the status write failed too, so the resource may still show its previous phase |
| `unable to get kubeconfig` (controller-runtime), `unable to start manager`, `unable to create controller`, `unable to set up health check`, `unable to set up ready check`, `problem running manager` | error | The process exits with code 1 |

Leader election itself records a `LeaderElection` Event on the Lease. Apart from that the operator
emits no Events.

**The brokers** log to stdout (`log_dest stdout`) at the types `error`, `warning`, `notice` and
`information`, as the generated configuration sets them
([README.md, the generated `mosquitto.conf`](../../README.md#the-generated-mosquittoconf)).
`spec.config` can add more destinations or types. Read them per pod with
`kubectl -n <ns> logs <name>-<ordinal>`. When the broker refuses a `spec.config`, the reason shows
here before the pod goes into `CrashLoopBackOff`.

## A renewed certificate

The operator does not watch the TLS Secret and cannot read it
([ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md)). When
cert-manager renews the certificate, or somebody replaces it by hand, nothing reaches the running
brokers. The kubelet updates the mounted files, because the mount uses no `subPath` (Kubernetes
behaviour, not verified here). The broker, though, reads them only at startup. Until the pods
restart, they keep serving the certificate they started with, and nothing in the operator marks the
moment: no Event, no condition, no log line.

**Restart the pods:**

```bash
kubectl -n messaging rollout restart statefulset/broker    # example names
```

This rolls one pod at a time ([how a roll proceeds](#which-changes-restart-the-broker-pods)), and
each pod picks up the new material when it restarts. The operator leaves the restart annotation in
place.

**Or point `spec.tls.secretName` at a new Secret.** The Secret's name is part of the pod spec, so
the change rolls the pods like any other template change. This is derived from the code; no test
renames a Secret.

**Nothing in the operator tracks the expiry.** Whatever renews the Secret also has to trigger the
restart. Otherwise a long-lived pod keeps serving its certificate after it has expired, and the
broker turns into an outage on a timer. The rotation path and its gaps are in
[rotation.md](../security/rotation.md).
