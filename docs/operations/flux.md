# Running brokers from Git with Flux

What Flux sees of a `Mosquitto` and its `MosquittoUser` objects, how a change committed to Git
reaches a running broker, and what holds it up. The files to commit and the Flux `Kustomization`
are in the [README fast start](../../README.md#-tldr-fast-start), step 4; the users and their
Secrets are [users.md](users.md).

**What this page rests on.** The README example was run on 2026-10-05 against the Kind cluster
`kind-mko-dev` (`kindest/node:v1.36.1`) with Flux `v2.8.6` (`source-controller` and
`kustomize-controller` only), the operator installed by Helm from this repository, and SOPS with
an age key. The source was an `OCIRepository` pushed with `flux push artifact` to a registry on
the Kind network, not a `GitRepository`; everything after the source — decryption, apply, prune,
health checks — is the same `Kustomization`. It has not run on a production cluster, and no
client application took part: the clients were `mosquitto_pub` in the broker pod, so the example's
NetworkPolicy was applied but its effect was not tested.

## Health checks need `healthCheckExprs`

Flux judges a custom resource with the generic `kstatus` rules unless it is given an expression.
Those rules read `status.observedGeneration` and the conditions `Reconciling` and `Stalled`, which
neither kind sets — **not** `Ready`. Observed with `healthChecks` naming the broker and both users:
the `Kustomization` reported `Health check passed in 13.469417ms` at 21:48:51, and the broker
turned `Ready` at 21:49:03. A plain health check therefore proves nothing about these kinds.

With the `healthCheckExprs` of the README — `current` when `status.observedGeneration` equals
`metadata.generation` and the `Ready` condition is `True` — Flux waits:

- **A change that rolls the broker.** A `spec.config` line added in Git: the `Kustomization` stayed
  `Healthy=Unknown`, `Running health checks for revision v2@…`, until the rolled pod was ready, and
  reported `Health check passed in 10.013102088s`.
- **A refused user.** A `MosquittoUser` whose Secret holds the username `mko-monitor`: the user
  reports `Ready=False`, reason `UsernameReserved`; with `wait: true` the `Kustomization` reported
  `health check failed after 1m0.009618237s: timeout waiting for:
  [MosquittoUser/home/monitoring status: 'InProgress']`. Flux names the object; the reason is on
  the object (`kubectl -n home get mqu monitoring -o yaml`).

`wait: true` checks every object the `Kustomization` applied. A `healthChecks` list checks only the
objects it names — a user missing from the list was not checked at all (observed: the refused user
above passed under a list that did not name it).

## A refused object holds back the next commit

Flux applies a new revision only when the previous reconcile has finished, and an unhealthy object
keeps that reconcile running until the `timeout`. Observed: a commit rotating a password was
fetched at 21:57:54 and applied at 22:00:54 — after the refused user's 3-minute health check had
timed out. **Fix or remove a refused object first**; until then every commit to the same
`Kustomization` waits up to its `timeout`. A shorter `timeout` shortens the wait and the time a
slow broker roll is given.

## A password rotated in Git

The encrypted file was decrypted, changed and re-encrypted, and the new revision pushed. Flux
applied it (`Secret/home/zigbee2mqtt-mqtt configured`, 22:00:54); the new password was accepted
42 seconds later and the old one refused with `Connection Refused: not authorised` (exit code 5).
The broker pod kept its UID and its `mosquitto` container its restart count `0`. No manual step
and no edit of a custom resource: the operator re-rendered `<broker>-auth`, the kubelet refreshed
the mount, the `reloader` signalled the broker ([users.md](users.md#how-long-a-change-takes)).

## A user deleted in Git

With `prune: true`, removing the user and its Secret from Git deletes both
(`MosquittoUser/home/monitoring deleted`, `Secret/home/monitoring-mqtt deleted`); the operator
renders the broker's users without it. Deleting the **last** user locks every client out — the
broker accepts nobody, and says so in its `Users` condition ([users.md](users.md)).

## The operator itself through Flux

Not run here. The chart installs both CRDs, so the `Kustomization` of the brokers has to wait for
the one that installs the operator (`dependsOn`), and the Secret grant the chart installs is a
choice to make before the first sync ([installation.md](installation.md#which-secrets-the-operator-may-touch)).
