# Installation

The operator is one Deployment. It reconciles `Mosquitto` resources in every namespace of the
cluster. There are two ways to install it:

- the Helm chart in [`deploy/helm/mosquitto-operator/`](../../deploy/helm/mosquitto-operator/);
- kustomize over [`config/default/`](../../config/default/), for people who do not want a chart in
  the loop.

Both paths grant the same authority, and `make verify-rbac-parity` renders both and compares them
([ADR 0006](../adr/0006-both-install-paths-grant-the-same-authority.md)). They are not identical
in other ways, and the differences are listed below.

Reference material lives in the README: the object names each path produces are in
[README.md, Operator install](../../README.md#operator-install), and the chart values and the
flags are in [Helm chart values](../../README.md#helm-chart-values) and
[Operator flags](../../README.md#operator-flags). Neither path has been applied to a real cluster
from this repository ([why](README.md)).

**Install exactly one operator per cluster.** The operator watches every namespace, and nothing
in [`cmd/main.go`](../../cmd/main.go) narrows it to a list. It takes its leader-election Lease in
its own namespace, because no `LeaderElectionNamespace` is set. So two installations (two Helm
releases, or a release next to a kustomize install) are two reconcilers writing the same objects,
and their Leases do not exclude each other.

## What each path installs

| | Helm chart | kustomize (`config/default`) |
|---|---|---|
| The CRD `mosquittoes.mko.gtrfc.com` | Rendered: [`templates/crd.yaml`](../../deploy/helm/mosquitto-operator/templates/crd.yaml) is an ordinary template. It is installed, upgraded and **deleted** with the release | Not rendered. Apply [`config/crd/bases/mko.gtrfc.com_mosquittoes.yaml`](../../config/crd/bases/mko.gtrfc.com_mosquittoes.yaml) yourself |
| The namespace | Any name. Use `--create-namespace` or an existing namespace | Fixed to `mosquitto-operator-system` by the overlay, and not rendered. Create it first |
| ClusterRole and ClusterRoleBinding | Rendered | Rendered |
| Leader-election Role and RoleBinding, and `--leader-elect` | Only while `leaderElection.enabled` is `true` (the default). The value switches the flag and the Role together | Always: [`config/manager/manager.yaml`](../../config/manager/manager.yaml) passes `--leader-elect` unconditionally |
| `--secret-security` and `get` on `secrets` | The value `secretSecurity` (default `false`) switches the flag and the rule together ([below](#which-secret-a-mosquitto-may-name)) | `--secret-security=false` in [`config/manager/manager.yaml`](../../config/manager/manager.yaml); the component [`config/components/secret-security`](../../config/components/secret-security/kustomization.yaml) appends `--secret-security=true` and adds the rule |
| The metrics server on `:8080` | Only while `metrics.enabled` is `true` (the default). `false` passes `--metrics-bind-address=0`, which starts no server | Always on. No flag is passed, so the binary's default `:8080` applies |
| A Service in front of the metrics port | `<fullname>-metrics`, while `metrics.enabled` | None |
| The operator image | `image.repository`, with `image.tag` defaulting to the chart's `appVersion` ([below](#install-with-helm)) | `controller:latest`, a placeholder you replace before applying ([below](#install-with-kustomize)) |
| Operator resources | The `resources` value | Fixed in the manifest: requests `10m` CPU / `64Mi` memory, limits `500m` / `128Mi` |
| Probe timings | Liveness after 15 s and then every 20 s; readiness after 5 s and then every 10 s | Path and port only. Kubernetes defaults apply to the rest |

Both paths harden the operator pod the same way: `runAsNonRoot`, the `RuntimeDefault` seccomp
profile, no privilege escalation, a read-only root filesystem, all capabilities dropped and a
10-second termination grace period. The runtime image is
`gcr.io/distroless/static-debian12:nonroot` ([`Containerfile`](../../Containerfile)). The manifest
comment states that these settings satisfy the restricted Pod Security Standard. That is read from
the manifests; no admission run confirms it.

The operator caches every ConfigMap, Service and StatefulSet in the whole cluster, not only the
ones it manages ([runtime.md](runtime.md#at-start)). Its memory therefore grows with the cluster,
and the two paths set different memory limits. Nobody has measured how much memory it needs.

## The namespace

**Helm** installs into whichever namespace `--namespace` names. The README's fast start uses
`mosquitto-operator-system`. **kustomize** always installs into `mosquitto-operator-system` and
creates no Namespace object, so `kubectl create namespace mosquitto-operator-system` comes first.
On both paths the leader-election Lease `mosquitto-operator.mko.gtrfc.com` and its namespaced Role
live in this namespace.

**Keep this namespace for the operator alone.** Its ServiceAccount is bound to a cluster-wide
ClusterRole that can `create` and `update` StatefulSets in every namespace. Anything that runs as
that ServiceAccount can therefore run any workload anywhere in the cluster
([privilege-footprint.md](../security/privilege-footprint.md)).

**Pod Security.** The operator pod is hardened as described above, so its namespace can carry
`pod-security.kubernetes.io/enforce=restricted`. Broker pods — the broker container and the
`config-check` init container — are built to the restricted standard in every shape the builder
can produce: plain, TLS, storage, both together, and hard anti-affinity. An API server's own
PodSecurity admission at `enforce=restricted` judges each of those pods in the integration tier
(`TestIntegration_PodSecurity_RestrictedAdmitsEveryShape` in
[`test/integration/pod_security_test.go`](../../test/integration/pod_security_test.go), against
envtest `1.29.0`), next to a unit test that spells the rules out
(`TestBuildStatefulSet_SatisfiesRestrictedPodSecurityStandard`). So the namespaces that hold
`Mosquitto` resources can enforce `restricted` too. If a broker pod is
rejected at admission, the StatefulSet is still created but no pods appear, and the resource stays
`Pending` ([runtime.md, status](runtime.md#status)).

## Install with Helm

The minimal command is [README.md, TL;DR fast start](../../README.md#-tldr-fast-start). From a
checkout:

```bash
helm install mosquitto-operator deploy/helm/mosquitto-operator \
  --namespace mosquitto-operator-system --create-namespace \
  --set image.tag=<version>        # example: see "Which image" below
```

**Which image a checked-out chart runs.** `image.tag` is empty by default, so the image tag is the
chart's `appVersion`, which is `0.1.0` in
[`Chart.yaml`](../../deploy/helm/mosquitto-operator/Chart.yaml). The release workflow
([`build.yml`](../../.github/workflows/build.yml), step "Update Chart Version") writes the release
version into `Chart.yaml` and `values.yaml` of the chart it packages and commits nothing back.

A chart installed from a checkout therefore runs `guidedtraffic/mosquitto-operator:0.1.0`, next
to the CRD and the RBAC of your checkout, unless you set `image.tag`. Set it to the release that
matches your tree, or build an image yourself:

```bash
make docker-build IMG=registry.example.com/mosquitto-operator:dev   # example; runs generate-all first
make docker-push  IMG=registry.example.com/mosquitto-operator:dev   # example
helm install mosquitto-operator deploy/helm/mosquitto-operator \
  --namespace mosquitto-operator-system --create-namespace \
  --set image.repository=registry.example.com/mosquitto-operator \
  --set image.tag=dev                                               # example
```

`docker-build` builds for the platform of the machine it runs on. `make docker-buildx` builds
`linux/amd64` and `linux/arm64` and pushes. The release workflow builds `linux/amd64` only.

**From the published chart repository.** When a GitHub release is published,
[`build.yml`](../../.github/workflows/build.yml) pushes the image
`guidedtraffic/mosquitto-operator:<version>` and the chart to
`https://guided-traffic.github.io/mosquitto-operator/`:

```bash
helm repo add mosquitto-operator https://guided-traffic.github.io/mosquitto-operator/
helm install mosquitto-operator mosquitto-operator/mosquitto-operator --version <version> \
  --namespace mosquitto-operator-system --create-namespace
```

This repository cannot show whether a chart and an image have actually been published for a given
version.

**Verify:**

```bash
kubectl -n mosquitto-operator-system rollout status deploy/mosquitto-operator
kubectl get crd mosquittoes.mko.gtrfc.com
kubectl -n mosquitto-operator-system logs deploy/mosquitto-operator   # "starting manager", then "Starting Controller"
```

Then create a broker as shown in [README.md, TL;DR fast start](../../README.md#-tldr-fast-start).
A `Ready` operator pod does not prove that it reconciles ([runtime.md, ports and probes](runtime.md#the-operators-ports-and-probes)).
A `Mosquitto` reaching `PHASE=Ready` does.

## Install with kustomize

```bash
kubectl apply -f config/crd/bases/mko.gtrfc.com_mosquittoes.yaml
kubectl create namespace mosquitto-operator-system
(cd config/manager && kustomize edit set image controller=guidedtraffic/mosquitto-operator:<version>)   # example image
kustomize build config/default | kubectl apply -f -
```

`make deploy IMG=<image>` runs the last two lines with the kustomize pinned in the
[`Makefile`](../../Makefile) (`make kustomize` installs it into `bin/`). Both forms edit
`config/manager/kustomization.yaml` in place. Revert that edit before you commit, or the
`generated-manifests` check fails on a dirty tree.

**Apply the CRD on its own, not through `make install`.** That target applies `config/crd` and
then `config/rbac` *without* the `config/default` overlay. The RBAC objects then carry no
`mosquitto-operator-` prefix, and the ServiceAccount, Role and RoleBindings target the literal
namespace `system`. That rendering is not what `config/default` installs, and the parity test does
not check it ([ADR 0006](../adr/0006-both-install-paths-grant-the-same-authority.md), Residual
risks).

**Verify:**

```bash
kubectl -n mosquitto-operator-system rollout status deploy/mosquitto-operator-mosquitto-operator
```

There is no value file on this path. Leader election, the metrics port and the resources are
whatever [`config/manager/manager.yaml`](../../config/manager/manager.yaml) says. To change one,
edit the manifest or add a patch to your own overlay. `secretSecurity` is the one setting shipped
as a component, because it changes the flag and the ClusterRole together:

```yaml
# your overlay's kustomization.yaml
resources:
  - <this repository>/config/default
components:
  - <this repository>/config/components/secret-security
```

## Leader election on the two paths

With `--leader-elect`, only the replica that holds the Lease `mosquitto-operator.mko.gtrfc.com` in
the operator's namespace runs the controller. The others wait, and their probes still answer
([runtime.md](runtime.md#the-operators-ports-and-probes)). The election uses controller-runtime's
defaults, which [`cmd/main.go`](../../cmd/main.go) does not change: a 15-second lease, a 10-second
renew deadline and a 2-second retry period. The leader does not release the Lease when it stops.
So after an operator restart or upgrade, the new pod reconciles only once the old lease has
expired, up to 15 seconds later.

**kustomize** always passes `--leader-elect` and always renders the Role. The comment in
`manager.yaml` names the one way this goes wrong: without the Lease permissions, the manager
starts, never acquires the Lease and retries forever. The pod is Ready and reconciles nothing. Only
an overlay that drops `leader_election_role.yaml` from the kustomization produces that state.

**Helm** passes the flag and renders the Role only while `leaderElection.enabled` is `true`, so
the chart cannot render one without the other. With it `false`, every operator replica runs the
reconciler and nothing coordinates them:

- **`replicaCount` above 1** means several writers on the same objects. A write that loses a
  conflict fails its pass, and the resource shows `Failed` until a later pass succeeds.
- **Every upgrade has an overlap.** The chart's Deployment sets no update strategy, so Kubernetes'
  default rolling update starts the new pod before it stops the old one. Without leader election,
  two operator versions reconcile side by side during that overlap. This follows from the
  Kubernetes default and was not observed.

The E2E suite installs the chart with `leaderElection.enabled: false`
([`test/e2e/helm-values.yaml`](../../test/e2e/helm-values.yaml)). So the default, with leader
election on, is a configuration that no test in this repository installs.

## Which Secret a `Mosquitto` may name

`spec.tls.secretName` names a Secret of the `Mosquitto`'s namespace, and the broker pod mounts it.
The author of a `Mosquitto` also chooses `spec.image`, the code that runs with that Secret
mounted. The install-time switch `secretSecurity` decides what that means
([ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md) D10):

| `secretSecurity` | Which Secret may be named | What it costs |
|---|---|---|
| `false` (default) | any Secret of the namespace | **`create` or `update` on `mosquittoes` in a namespace is reading every Secret of that namespace.** Grant `mosquittoes` no more widely than Secrets |
| `true` | only a Secret labelled `mko.gtrfc.com/consumable=true` | the ClusterRole gains `get` on `secrets`, cluster-wide, for a metadata-only read of that label; label every TLS Secret a broker serves, `kubectl label secret <name> mko.gtrfc.com/consumable=true` |

Whoever may label a Secret may write it, so the label is the Secret owner's consent. With `true`, a
`Mosquitto` naming an unlabelled or missing Secret is `Failed` with reason `SecretNotConsumable`
or `SecretNotFound`, nothing is written, and the check repeats every minute. Turning the switch on
for a cluster whose TLS Secrets are not labelled yet stops every TLS broker from being updated —
running pods keep running — until the labels are there; label first, then switch.

## TLS for the brokers

A broker serves MQTTS from a Secret that `spec.tls.secretName` names. The Secret must be in the
`Mosquitto`'s own namespace and must carry `tls.crt` and `tls.key`. **The operator never creates,
renews, reads or watches that Secret's data**; with `secretSecurity: true` it reads the Secret's
labels and nothing else ([ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md),
[above](#which-secret-a-mosquitto-may-name)). The kubelet
mounts the Secret. Two ways of filling it are first class, and neither involves this project.

**By hand:**

```bash
kubectl -n messaging create secret tls broker-tls --cert=tls.crt --key=tls.key   # example names
```

**With a cert-manager `Certificate` that you own.** This works on a cluster that already runs
cert-manager. Nothing this project ships installs cert-manager or depends on it. `make
cert-manager-install` is a fixture for the E2E suite and is not an install step. The shape below
is the one the E2E suite writes (`createCertificate` in
[`test/e2e/tls_test.go`](../../test/e2e/tls_test.go)):

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: broker-tls                                          # example
  namespace: messaging                                      # example: the Mosquitto's namespace
spec:
  secretName: broker-tls                                    # example: = spec.tls.secretName
  dnsNames:
    - broker.messaging.svc.cluster.local                    # example: the client Service
    - broker-0.broker-headless.messaging.svc.cluster.local  # example: one pod, if clients dial it
  issuerRef:
    name: my-ca-issuer                                      # example: an issuer your cluster runs
    kind: ClusterIssuer
    group: cert-manager.io
```

The certificate must name the host each client dials. The addresses follow from the
`Mosquitto`'s name ([README.md, Addresses](../../README.md#addresses)). The E2E suite lists each
name explicitly rather than using a wildcard, because `mosquitto_pub` verifies the hostname, and a
name missing from the SAN list fails the handshake with an error that looks like a broker defect.

**What the operator does with it:**

- It mounts the whole Secret read-only at `/mosquitto/tls`. Every key appears there, so the
  `ca.crt` a CA issuer adds is available to clients inside the pod.
- The generated configuration names only `tls.crt` and `tls.key`.
- The single listener moves from `1883` to `8883`, on the container and on both Services. Clients
  have to change their port and scheme with it.

**What it checks:** nothing. If the Secret is missing, the StatefulSet is written anyway
(`TestIntegration_TLS_DoesNotWaitForTheSecret`). The pod then waits on the kubelet's volume mount,
and the resource stays `Pending` instead of turning `Failed`. A certificate and key that do not
match are read only by the broker at startup. Neither case has been observed on a cluster. In both,
look at the pod's events: `kubectl -n <ns> describe pod <name>-0`.

**What TLS gives you:** an encrypted connection and a broker that proves its identity to clients.
It authenticates no client. The generated broker accepts anonymous clients with or without TLS
([README.md, Two modes](../../README.md#two-modes-and-what-each-one-protects),
[ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md),
[trust-boundaries.md](../security/trust-boundaries.md)).

**A renewed certificate does not reach a running broker.** [runtime.md, a renewed
certificate](runtime.md#a-renewed-certificate) explains what to do.

## Storage

**Without `spec.storage`**, the persistence directory `/mosquitto/data` is an `emptyDir`. It lasts
as long as the pod does. When a pod is deleted or rescheduled, the replacement starts empty and
loses its retained messages and persistent sessions. Every roll of the StatefulSet replaces every
pod ([runtime.md](runtime.md#which-changes-restart-the-broker-pods)), so it loses all of them.
That is Kubernetes `emptyDir` behaviour.

**With `spec.storage`**, every broker pod gets its own claim from the `data` template, named
`data-<name>-<ordinal>` ([README.md, Objects](../../README.md#objects)). The claim's access mode is
`ReadWriteOnce`. An empty or absent `storageClassName` uses the cluster's default class. On a
cluster without one, the claim cannot bind and the pod stays `Pending` (Kubernetes behaviour, not
observed here). The pod's `fsGroup: 1883` is what makes the volume writable for the broker, which
runs as uid `1883` (the comment on `brokerUserID` in
[`internal/builder/statefulset.go`](../../internal/builder/statefulset.go)). A volume type that
does not apply `fsGroup` leaves the directory owned by root. The broker then cannot write its
persistence file and still reports Ready, because readiness is a TCP check
([runtime.md, status](runtime.md#status)).

**Decide on storage before you create the resource.** The claim template is written once, when the
StatefulSet is created, and is never updated. Changing `spec.storage` afterwards needs the
StatefulSet recreated by hand
([runtime.md](runtime.md#changing-specstorage-on-an-existing-broker)).

**Claims outlive the broker.** The StatefulSet sets no claim retention policy, and the operator has
no permission on `persistentvolumeclaims`
([ADR 0009](../adr/0009-delete-only-through-owner-references.md), Consequences). Deleting a
`Mosquitto`, scaling it down or uninstalling the operator leaves every claim in place. A new
`Mosquitto` with the same name in the same namespace gets the same claims back, data included.
Deleting them is a manual step:

```bash
kubectl -n messaging get pvc                        # example namespace
kubectl -n messaging delete pvc data-broker-0       # example: the claim of pod broker-0
```

## Upgrade

**Helm.** The CRD is a template of the chart, so a chart upgrade moves the schema, the permissions
and the operator image together:

```bash
helm upgrade mosquitto-operator deploy/helm/mosquitto-operator \
  --namespace mosquitto-operator-system --set image.tag=<version>     # example
# or, from the chart repository:
helm upgrade mosquitto-operator mosquitto-operator/mosquitto-operator --version <version> \
  --namespace mosquitto-operator-system
```

Updating the operator image alone, with `kubectl set image` or a newer tag against an older chart,
leaves the CRD and the ClusterRole behind and is not a supported upgrade.

**kustomize.** Repeat the install: apply the CRD file of the new tree, set the new image, and apply
`config/default`.

**What an upgrade does to running brokers.** The broker pods contain no operator image and no
sidecar, so a new operator version restarts nothing just by running. The first upgrade to the
release that added the `config-check` init container is the exception that proves the rule: every
pod spec changed, so every broker rolls once. When it starts, though, it
gives every `Mosquitto` in the cluster a pass ([runtime.md](runtime.md#at-start)). That pass rolls
a broker's pods whenever the new version renders a different pod spec or a different
`mosquitto.conf` for it than the version before, because both are hashed onto the pod template.

The case to watch is the default broker image. A `Mosquitto` that names no `spec.image` runs the
operator's pin, `builder.DefaultImage`, which Renovate moves within the 2.x line
([ADR 0007](../adr/0007-one-broker-image-pin-and-why-not-the-openssl-tag.md)). A release that moves
the pin rolls every such broker in the cluster on its first pass after the upgrade, all at about
the same time. **To decide when a broker restarts, set `spec.image` explicitly.** It then moves
only when you change it.

**Rollback.**

```bash
helm rollback mosquitto-operator --namespace mosquitto-operator-system
```

The CRD is part of the release, so a rollback also restores the previous CRD schema. The API server
prunes spec fields that only the newer schema knows from existing resources (Kubernetes behaviour,
not observed here). So roll back before you adopt new fields, or re-apply them after upgrading
again. A rollback is a version change like
any other, and it rolls brokers under the same rule as an upgrade.

## Uninstall

**Uninstalling the chart deletes every broker in the cluster.** The CRD is a plain template with no
`helm.sh/resource-policy: keep`, so `helm uninstall` deletes it. Deleting the CRD deletes every
`Mosquitto` in every namespace, and the garbage collector then removes the StatefulSets, Services
and ConfigMaps they own. The operator registers no finalizer, so nothing waits for it, and the
order in which the operator and the CRD disappear makes no difference to the cleanup
([ADR 0009](../adr/0009-delete-only-through-owner-references.md)). Delete the resources first so the
deletion is a step you take knowingly:

```bash
kubectl get mq --all-namespaces                     # what is about to go
kubectl delete mq --all --all-namespaces
helm uninstall mosquitto-operator --namespace mosquitto-operator-system
```

**kustomize:**

```bash
kustomize build config/default | kubectl delete -f -               # the operator and its RBAC; brokers keep running, unmanaged
kubectl delete -f config/crd/bases/mko.gtrfc.com_mosquittoes.yaml  # every Mosquitto, and with it every broker
```

`make undeploy` runs the first line. `make uninstall` deletes `config/rbac` *without* the overlay,
which removes the unprefixed objects in `system` and not the ones `config/default` installed. It
then deletes the CRD.

**What stays behind on both paths:** the claims of every broker that had `spec.storage`
([above](#storage)), every TLS Secret and cert-manager `Certificate` you created, and the operator's
namespace. On the Helm path, `helm uninstall` does not delete a namespace that `--create-namespace`
made (Helm behaviour).

Garbage collection of the owned objects is the intended mechanism, but no run of it has been
observed from this repository. The E2E subtest that checks it is in the tree and not run by CI.

**To stop the operator without losing the brokers**, scale its Deployment to zero instead of
uninstalling it ([runtime.md, when the operator is not running](runtime.md#when-the-operator-is-not-running)).
