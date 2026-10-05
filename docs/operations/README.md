# Operating the Mosquitto Operator

These pages are for the people who install the operator and keep it running.
[README.md](../../README.md) is the short version: what the operator is, how to start it, the
naming conventions, and the complete reference for the `Mosquitto` resource, the Helm values and
the operator flags. The README links here for detail. A page here explains a setting; it never
restates the reference tables, which live in the README and nowhere else.

| Page | Read it when |
|---|---|
| [installation.md](installation.md) | You are installing the operator, through the Helm chart or through kustomize. It covers what each path renders and what it leaves to you, the CRD and the namespace, which image a checked-out chart runs, leader election on both paths, the TLS Secret a broker serves from, storage classes and the claims that outlive a broker, upgrading, rolling back and uninstalling |
| [users.md](users.md) | You are giving clients access to a broker. It covers the `MosquittoUser` and its Secret, what the operator renders from them and how the broker picks a change up without a restart, how long a new user, a rotated password or a revocation takes, which Secrets the operator reads, and who can reach the broker at all |
| [flux.md](flux.md) | You run brokers and users from Git with Flux. It covers why the health checks need `healthCheckExprs`, how a refused object holds back the next commit, and what a password rotated or a user deleted in Git does — observed on Kind with Flux |
| [runtime.md](runtime.md) | You want to know what the operator does when it starts and what one reconcile pass writes or leaves alone. It also covers which `spec` changes restart the broker pods, changing `spec.storage` on a running broker, what `Pending`, `Progressing`, `Ready` and `Failed` mean, the operator's ports and probes, what happens while it is not running, what it logs, and how a renewed certificate reaches a broker |

**What these pages rest on.** Every statement about the operator was read from this repository's
code, manifests and tests. The E2E suite in [`test/e2e/`](../../test/e2e/) installs the chart into
a Kind cluster, runs brokers, speaks MQTT to them and watches their objects collected; it runs on
every pull request and before every release, and was first observed passing on 2026-10-05. Kind is
the only cluster any of it has run on: the Helm install path, a rollout and garbage collection have
a recorded run behind them there, the kustomize path and every production-cluster property
(CNI, storage driver, admission chain) do not. Where a page describes what Kubernetes itself does
with the objects the operator writes beyond what the suite checks, it says so.

**What the brokers are.** One `Mosquitto` runs independent Mosquitto processes behind one Service.
There is no bridging, no shared sessions, no shared retained messages and no clustering. More
replicas give you process redundancy, not a highly available broker. Every restart of a broker
pod described in these pages therefore drops that pod's clients, and its sessions and retained
messages stay with that pod.

## The other documentation

| Where | What |
|---|---|
| [README.md](../../README.md) | What the operator is, the fast start, the naming conventions, and the complete reference |
| [docs/security/](../security/README.md) | Trust boundaries, where the credentials live, the privilege footprint, and what each mechanism leaves open |
| [docs/adr/](../adr/README.md) | Why the operator behaves the way it does, and what was rejected |
| [docs/developer/](../developer/README.md) | Changing the code: the repository layout, the reconcile pipeline, the test tiers and the release process |
