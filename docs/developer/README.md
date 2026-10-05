# Developer documentation

The contributor entry point and the overviews for people changing this operator. The detail is in
the code; what lives here is the shape of things — how the pieces fit, which invariants hold, why
a design that looks odd is the way it is, and the workflow around it.

**What belongs here:** anything a future developer needs before touching a part of the operator
that the code cannot state on its own, and everything about contributing: the layout, the build
and test matrix, continuous integration and the release, the checklists, the conventions.

**What does not:** decisions (those are [ADRs](../adr/README.md)), the work list (that is
[the project plan](../planning/project-plan.md)), what somebody running the operator needs (that is
[docs/operations/](../operations/README.md), with the reference tables — the CRD fields, the
chart values, the operator flags, the deterministic names — in [README.md](../../README.md)), and
the security design (that is [docs/security/](../security/README.md)). There is no `DEVELOPER.md`
at the root any more; this directory replaced it.

A page here **may and should** point at files and functions. That is the point of it. It also
means it goes stale when the tree moves, so whoever moves the tree updates the page in the same
change.

## What has to be in your head first

- **Two kinds, five objects.** A `Mosquitto` (`mko.gtrfc.com/v1`, resource `mosquittoes`, short
  name `mq`) produces the Secret `<name>-auth`, a ConfigMap, a headless Service, a client Service
  and a StatefulSet — the whole managed set ([architecture.md](architecture.md#what-one-pass-writes)).
  A `MosquittoUser` (`mosquittousers`, `mqu`) is never written by the operator except its status;
  it is rendered into its broker's `<name>-auth`.
- **Every broker requires a login.** No anonymous access, no switch for it; `spec.config` takes an
  allowlist of tuning directives ([ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)).
- **Secrets: stripped in the cache, read on demand.** The cache holds Secret metadata only; a
  credentials Secret's data is read with one uncached `get` when its user is rendered, and a TLS
  Secret's data never ([architecture.md](architecture.md#the-reconcile-pipeline)).
- **Independent brokers, not a cluster.** The pods are separate Mosquitto processes behind one
  Service: no bridging, no shared sessions, no shared retained messages. `spec.replicas` buys
  process redundancy, not a highly available broker. Do not write a line that implies otherwise
  ([architecture.md](architecture.md#what-runs-where)).
- **Pure builders and renderer, one loop, one binary with two entry points.**
  [`internal/builder`](../../internal/builder) turns a CR into objects and
  [`internal/auth`](../../internal/auth) users into credentials, both without a client;
  [`internal/controller`](../../internal/controller) is the only code that talks to the API
  server; [`internal/reloader`](../../internal/reloader) is `manager reload`, which runs in every
  broker pod ([package-map.md](package-map.md)).
- **Ownership before every write, deletion only through owner references.** `ensureOwned` refuses
  any object this CR does not control; the ClusterRole has no `delete` and no `patch`; a CR being
  deleted gets no writes ([ADR 0009](../adr/0009-delete-only-through-owner-references.md)).
- **Hashes, not structural diffs.** The StatefulSet is compared by replicas, labels and two hash
  annotations; `mko.gtrfc.com/config-hash` is what carries a config change into a roll, because
  Mosquitto reads its file once. `volumeClaimTemplates` are never updated
  ([architecture.md](architecture.md#how-a-change-reaches-a-running-broker)).
- **The markers are the source.** The CRD and `config/rbac/role.yaml` come from the kubebuilder
  markers through `make generate-all`; the chart's ClusterRole is hand-written, and
  `make verify-rbac-parity` compares what both install paths render
  ([ADR 0006](../adr/0006-both-install-paths-grant-the-same-authority.md)).
- **One broker image pin, in two constants held equal by a test** — `builder.DefaultImage` and
  `testimages.MosquittoImage`, `eclipse-mosquitto:2.1.2-alpine`
  ([ADR 0007](../adr/0007-one-broker-image-pin-and-why-not-the-openssl-tag.md)). What that image
  actually does is measured in [broker-behaviour.md](broker-behaviour.md).
- **`make` is the entry point**, locally and in CI; tools resolve to `bin/`, never to `PATH`
  ([build-test-lint.md](build-test-lint.md)).
- **Five test tiers, by build tag**, and nothing is skipped: no `-short`, no `testing.Short()`.
  envtest runs no kubelet and no garbage collector, so what needs either is an E2E test
  ([testing.md](testing.md)).
- **A check is not a check until it has failed on purpose**
  ([ADR 0010](../adr/0010-a-check-is-not-a-check-until-it-has-failed-on-purpose.md)).
- **The only cluster this operator runs on is Kind.** The unit, integration, image-tools and
  RBAC-parity tiers need none; the E2E tier creates a Kind cluster per leg, on every pull request
  and before every release ([ci-and-release.md](ci-and-release.md#the-e2e-jobs)). It was first
  observed passing on 2026-10-05; nothing has been observed on a production cluster.
- **English only**, in code, comments, commits and documentation.

| Page | Read it when |
|---|---|
| [repository-layout.md](repository-layout.md) | You are new and want the tree |
| [package-map.md](package-map.md) | You are looking for where something lives and what it is responsible for |
| [architecture.md](architecture.md) | You want the picture: what runs where, what happens at start, what one reconcile pass does and writes, how status is computed, how a change reaches a running broker |
| [broker-behaviour.md](broker-behaviour.md) | You need to know what the pinned broker image actually does — measured, with method |
| [build-test-lint.md](build-test-lint.md) | You want to build, generate, lint or test anything locally: the Make targets, the tool pins, what was last recorded, coverage |
| [testing.md](testing.md) | You are adding a test, choosing a tier, or a suite is failing and you need to know what it is for and what it needs |
| [ci-and-release.md](ci-and-release.md) | You touch a workflow, Renovate or the release |
| [adding-things.md](adding-things.md) | You add a CRD field, a managed object, an E2E scenario or a Renovate-managed pin |
| [conventions.md](conventions.md) | You write a commit, Go, a comment, a test, a guard or documentation |

## Core flows, one fact each

| Flow | The fact | Where |
|---|---|---|
| Operator start | Flags parsed, zap logger set, the manager built with Lease ID `mosquitto-operator.mko.gtrfc.com` in its own namespace, the controller registered, `healthz` and `readyz` as plain pings, `mgr.Start` | [architecture.md](architecture.md#operator-startup) |
| What wakes the loop | A generation change of a `Mosquitto` or of a `MosquittoUser` naming it (status writes do not), any change of an owned object, or of a Secret a user or its TLS listener names; up to 4 resources at once, one pass per resource at a time | [architecture.md](architecture.md#watches-and-concurrency) |
| A reconcile pass | Get the CR (gone: its users get `BrokerNotFound`; deleting: no writes), the refusals, the users rendered into `<name>-auth` and their statuses, then ConfigMap, headless Service, client Service, StatefulSet in that order, then status | [architecture.md](architecture.md#the-reconcile-pipeline) |
| A write | `SetControllerReference`, `Get`, `Create` on NotFound, otherwise `ensureOwned` and a semantic diff before `Update` — never `Patch`, never `Delete` | [architecture.md](architecture.md#what-each-write-compares) |
| A foreign object on a generated name | Refused, never adopted: the pass stops, `phase: Failed`, `Ready=False`, reason `ReconcileFailed`, and the error goes back to the work queue | [ADR 0009](../adr/0009-delete-only-through-owner-references.md) |
| Status | `phase`, `observedGeneration` and the one `Ready` condition written together by `setPhase`, and only when they changed; `Failed` describes the operator, not the brokers | [architecture.md](architecture.md#status) |
| A config change | The ConfigMap is updated and the config hash on the pod template changes, which is what rolls the pods | [architecture.md](architecture.md#how-a-change-reaches-a-running-broker) |
| A replica change | Only `spec.replicas` of the StatefulSet moves; nothing rolls | [architecture.md](architecture.md#how-a-change-reaches-a-running-broker) |
| A `MosquittoUser` added, changed or deleted, or its Secret changed | The broker's pass re-renders `<name>-auth`; the kubelet refreshes the mount, the reloader copies it in and sends `SIGHUP`; nothing rolls | [architecture.md](architecture.md#the-credentials-path) |
| A rotated TLS Secret | Nothing: the operator never reads its content, and running pods serve the old material until they restart | [ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md) |
| Deleting a `Mosquitto` | The reconciler writes nothing; the garbage collector removes the five objects through their controller references; its users report `BrokerNotFound` | [ADR 0009](../adr/0009-delete-only-through-owner-references.md) |
| An RBAC change | Marker in the controller, `make generate-all` for `config/rbac/role.yaml`, the chart's ClusterRole by hand, `make verify-rbac-parity` | [adding-things.md](adding-things.md#a-managed-object) |
| A pull request | Fourteen jobs on self-hosted runners — two E2E legs and their gate among them — each check entered through a Make target | [ci-and-release.md](ci-and-release.md#test-and-release) |
| A release | On a push to `main`, after thirteen of those jobs, `semantic-release` cuts the version from Conventional Commits with a GitHub App token; the published release builds and pushes the image and publishes the chart to `gh-pages` | [ci-and-release.md](ci-and-release.md#the-release) |
| A dependency update | Self-hosted Renovate nightly; minor, patch and digest updates automerge after CI, majors wait for a human; `make verify-ci-references` proves every customManager still matches | [ci-and-release.md](ci-and-release.md#renovate) |

## What has no page here

Not built, so no page: the broker metrics exporter (recorded in
[ADR 0002](../adr/0002-the-metrics-exporter-is-written-here.md), nothing of it implemented),
authentication and ACLs in the API (`spec.config` is where they go today,
[ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)),
admission webhooks, PodDisruptionBudgets, NetworkPolicies, ServiceMonitor and PrometheusRule
([architecture.md](architecture.md#what-is-not-built)). The operator's own metrics endpoint is a
section of [architecture.md](architecture.md#the-operators-own-metrics-endpoint), the generated
`mosquitto.conf` another ([architecture.md](architecture.md#the-generated-mosquittoconf)). Each
subsystem gets its page here when it exists.
