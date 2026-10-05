# Users, credentials and who can log in

How a client gets onto a broker, how a change of its credentials or rights reaches the running
broker, how long that takes, and what the operator reads to do it. The fields and reasons are in
the [README reference](../../README.md#the-mosquittouser-resource-fully-populated); the decisions are
[ADR 0013](../adr/0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md) and
[ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md).

## A client is a `MosquittoUser` and a Secret

Every broker requires a login; there is no anonymous access and no switch for it. A client is two
objects in the broker's namespace: a Secret with its username and password, and a
`MosquittoUser` that names the broker, the Secret and the topics the client may read and write.
A `kubernetes.io/basic-auth` Secret works without key configuration, and the client application
can mount or reference the same Secret — one credential, one place:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: homeassistant-mqtt
  namespace: home                      # example
type: kubernetes.io/basic-auth
stringData:
  username: homeassistant
  password: change-me                  # example — SOPS-encrypted in Git
---
apiVersion: mko.gtrfc.com/v1
kind: MosquittoUser
metadata:
  name: homeassistant
  namespace: home
spec:
  brokerRef:
    name: broker
  credentialsSecret:
    name: homeassistant-mqtt
  acls:
    - topic: homeassistant/#
      access: readwrite
    - topic: zigbee2mqtt/#
      access: readwrite
```

The username comes from the Secret, so the object's name does not have to match it and a
migration keeps the usernames its clients already use; `status.username` shows what was read. Two
users of one broker with the same username: the older one keeps it, the newer one reports
`UsernameConflict` naming the holder. `$SYS` and every other `$` topic are refused, so monitoring
through `$SYS` is not something a `MosquittoUser` can be given.

**What a broker without users does.** It runs and accepts nobody. Its `Users` condition is `False`
with reason `NoUsers`, and `kubectl get mq` shows `USERS 0`. Deleting the last user of a broker in
Git — a Flux prune, a moved directory — therefore locks every client out instead of opening the
broker.

## What the operator does with them

On every pass for a broker the operator lists its users, reads each user's Secret, and renders all
of them into one Secret it owns, `<broker>-auth`, with two keys: `passwd` (one
`username:$7$...` line per user, PBKDF2-SHA512 at 1000 iterations) and `acl` (one `user` block per
user). An unchanged password keeps its hash, so a pass that changes nothing writes nothing. Each
user then gets its `Ready` condition: `True` with reason `Accepted` once it is in `<broker>-auth`,
`False` with a reason otherwise.

Inside the broker pod:

1. `auth-init`, an init container of the operator's own image, copies `passwd` and `acl` from the
   mounted `<broker>-auth` into an `emptyDir` at `/mosquitto/auth`, as `1883:1883` mode `0600`,
   on every start of the pod;
2. the broker reads them there through the `password-file` and `acl-file` plugins;
3. `reloader`, a sidecar of the same image, compares the mounted Secret with the copies every two
   seconds; after the kubelet has refreshed the mount it copies the change in through a temporary
   file and a rename and sends the broker `SIGHUP`. Its log says `credentials changed, copied` and
   `signalled the broker to reload`; the broker's says `Reloading config.`

Nothing restarts. What a reload does, measured against the pinned image
([broker-behaviour.md](../developer/broker-behaviour.md) M2, M14):

| Change in Git | Effect on the running broker |
|---|---|
| A user added | Can log in after the reload |
| A password changed in the Secret | The old password is refused; connections made with it are dropped at the reload; the new one works |
| A user deleted, or its Secret deleted | Its open connections are dropped at the reload; it can no longer log in |
| An ACL narrowed or widened | Applies to the next message on every open connection; nobody is disconnected |

`kubectl logs <pod>` and `kubectl exec <pod>` pick the broker container; the reloader's log is
`kubectl logs <pod> -c reloader`.

## How long a change takes

A change reaches the broker when the kubelet refreshes the mounted `<broker>-auth`, which the
operator does not control: **69 to 84 seconds** after the Secret changed, measured three times on
an idle Kind node with default kubelet settings
([broker-behaviour.md](../developer/broker-behaviour.md) M23), plus up to two seconds of the
reloader's poll. A loaded node or a kubelet with a longer sync period takes longer; nothing here
measured that. A user's `Ready=True` therefore says "rendered", not "already loaded" — the E2E suite
waits up to four minutes for a change to arrive (`reloadTimeout` in
[`test/e2e/users_test.go`](../../test/e2e/users_test.go)).

**Revocation has the same delay.** A deleted user or a rotated password keeps working for that
minute. There is no way to cut a client off sooner without restarting the broker pod
(`kubectl delete pod <broker>-0`), which drops every client.

## Which Secrets the operator touches

It reads the Secrets the users name, writes `<broker>-auth`, and never reads a TLS Secret's data.
It caches Secrets without their data — names, labels and owners only — and reads a Secret's data
with one direct request when a pass renders that user. Where it may do that is an install-time
choice ([installation.md](installation.md#which-secrets-the-operator-may-touch)):

- **`secretAccess.mode: all`** (the default) — in every namespace. The README states the grant
  before the install command and the chart prints it.
- **`secretAccess.mode: namespaces`** — only in the listed namespaces. A `Mosquitto` elsewhere is
  `Failed` with reason `NamespaceNotGranted`, and so are its users.

And which Secret a user may name is `secretSecurity`: with `false` any Secret of its namespace,
with `true` only one labelled `mko.gtrfc.com/consumable=true`
(reason `SecretNotConsumable` otherwise).

## Who can reach the broker

The operator ships no NetworkPolicy
([ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
D16): every pod of the cluster can reach the broker's pod IP and try passwords, and without
`spec.tls` it can read what crosses the network there. A policy of your own narrows that; it
depends on your CNI enforcing NetworkPolicy, which this repository has not tested:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: broker-clients
  namespace: home                                  # example
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/instance: broker           # the broker's name
      app.kubernetes.io/managed-by: mosquitto-operator
  policyTypes: [Ingress]
  ingress:
    - from:
        - podSelector:
            matchLabels:
              mqtt.example.com/client: "true"     # example — label your clients
      ports:
        - port: 1883                               # 8883 with spec.tls
```
