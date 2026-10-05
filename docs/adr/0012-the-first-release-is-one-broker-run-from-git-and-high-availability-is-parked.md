# ADR 0012: The First Release Is One Broker Run From Git, and High Availability Is Parked

## Status

Accepted. Date: 2026-10-05. Decided by the owner in a round of questions that began on
2026-09-01 with a plan aimed at high availability, user management and uninterrupted upgrades,
and was re-cut on 2026-10-05: high availability "seems harder than expected", and the first
release is to make one concrete migration easy — moving the MQTT broker of a Home Assistant
installation into a cluster managed by Flux.

*Amended 2026-10-05:* D3 — HA comes last, after every other phase of the plan.

**Built 2026-10-05:** D5 — `spec.podLabels` and `spec.podAnnotations`, merged under the
operator's keys in `BuildStatefulSet` and observed reaching the pods on Kind
(`TestE2E_PodMetadata_ReachesAndLeavesThePods`, which against the operator of `main` before this
change failed with `no ready broker pod of e2e-pod-metadata/broker carries the label and the
annotation of the CR`); and D4's guard — an API server's PodSecurity
admission at `enforce=restricted` judges the pod of every shape the builder renders
(`TestIntegration_PodSecurity_RestrictedAdmitsEveryShape`, envtest `1.29.0`, which enforces
PodSecurity: the test's control pod is refused). Observed failing once with
`allowPrivilegeEscalation: true` on the broker container: `pods "broker-0" is forbidden: violates
PodSecurity "restricted:latest": allowPrivilegeEscalation != false (container "mosquitto" must set
securityContext.allowPrivilegeEscalation=false)`. The `config-check` init container is the first
container added since D4 and passes the same guard.

**Built 2026-10-05 as well:** R1 and R2 (`MosquittoUser`, [ADR 0013](0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md)),
R3 for users (a new user, a changed password, a removed user and a changed ACL reach the running
broker without a restart, [ADR 0014](0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md)),
R5 for both kinds (each reports `observedGeneration` and a `Ready` condition with a reason, a user
applied before its broker or its Secret converges through the watches —
`TestIntegration_Users_ConvergeWhenTheirReferencesArrive` — and one user's failure leaves the broker
and the other users alone — `TestReconcile_EveryUserReason`), and R6 for every container now in a
broker pod (D4). **Not built:** R3 for a renewed certificate
([ADR 0001](0001-the-operator-consumes-tls-material-it-never-issues-it.md) D10), and D1's migration,
which is the owner's to run. The work is ordered in [the project plan](../planning/project-plan.md).

## Context

`v0.1.0` renders four objects per `Mosquitto` — a ConfigMap, two Services, a StatefulSet — and
every broker is anonymous ([ADR 0008](0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)).
The first plan wanted three things at once: highly available brokers, users with permissions
that change at runtime, and version updates without interruption.

Research and measurement showed the first one to be a different project. Open-source Mosquitto
has no clustering: no shared sessions, no shared retained messages, no failover; `replicas: 3`
is three brokers that do not know about each other. What is reachable is one active broker with
a bounded failover, or a bridged pair that replicates retained state but not sessions — each a
product decision about which property is worth which cost, and none of them needed for the
migration that is actually waiting. The research and its open questions are parked in
[docs/planning/ha-research.md](../planning/ha-research.md).

What makes that migration hard today is concrete: several clients with different read and write
rights, each with its own password, kept in Git and applied by Flux, and the broker following
every change — a new password, a new user, a new pod label — without a manual step.

## Decision

**D1 — The goal of the next release is an operator that makes running a Mosquitto broker from
Git through Flux easy.** Success is measured against one migration: the broker of a Home
Assistant installation and its clients (Home Assistant, Zigbee2MQTT and similar), moved into the
cluster without hand-written configuration and without manual steps after a change in Git.

**D2 — Six requirements define the release.**

| ID | Requirement | Where it is decided |
|---|---|---|
| R1 | Several users, each with its own read / write / readwrite permissions per topic | [ADR 0013](0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md) |
| R2 | Each user's username and password live in **its own Secret**, as plain values a client can consume too | ADR 0013 D2 |
| R3 | Changes apply themselves: a changed password, an added or removed user, a changed ACL and a renewed certificate reach the running broker without a manual step and without a restart | [ADR 0014](0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md), [ADR 0001](0001-the-operator-consumes-tls-material-it-never-issues-it.md) D10 |
| R4 | A pod label or annotation set on the `Mosquitto` reaches the broker pods | D5 |
| R5 | Flux can tell success from failure | D6 |
| R6 | Maximum pod security from the first release | D4 |

**D3 — High availability and everything multi-replica is parked.** `spec.replicas` keeps its
`v0.1.0` meaning — independent brokers, no shared state, no shared sessions — and nothing built
for this release may rely on `replicas > 1`. The parked research holds the open questions
(HA1–HA7) that must be answered before any HA mechanism is built. ~~It is reopened by the owner's
call, at the latest when a client needs a bounded failover the single broker cannot give.~~
*(Amended 2026-10-05 by the owner:)* **High availability is the last phase of the plan**, started
only when every other phase is done; no HA question is put to the owner before then.

**D4 — Every container the operator renders meets the restricted Pod Security Standard and runs
as uid `1883`, from the first release on.** No root, no capabilities, no privilege escalation, a
read-only root filesystem, the `RuntimeDefault` seccomp profile and no ServiceAccount token. This
holds for the broker container today (`buildPodSpec` and `buildBrokerContainer` in
[`internal/builder/statefulset.go`](../../internal/builder/statefulset.go)) and for the operator's
own pod on both install paths ([`config/manager/manager.yaml`](../../config/manager/manager.yaml),
[`deploy/helm/mosquitto-operator/templates/deployment.yaml`](../../deploy/helm/mosquitto-operator/templates/deployment.yaml)),
and it binds every init container and sidecar added later — the reload sidecar of ADR 0014 signals
the broker without `CAP_KILL` because it shares the broker's uid. Today a unit test spells the
restricted rules out by hand ([`internal/builder/statefulset_test.go`](../../internal/builder/statefulset_test.go));
the release adds a guard in which an API server's PodSecurity admission at `enforce=restricted`
judges a rendered pod, observed failing once against a weakened security context
([ADR 0010](0010-a-check-is-not-a-check-until-it-has-failed-on-purpose.md)).

**D5 — `spec.podLabels` and `spec.podAnnotations` are merged under the operator's own labels and
annotations.** A key the operator sets — the selector labels, the version label, the two hash
annotations — always wins, so a user cannot detach a Service from its pods or forge a hash. A
change rolls the pods through the pod template, which `StatefulSetHasChanged` already compares by
template labels and the pod-spec hash.

**D6 — Every kind reports `status.observedGeneration` and a `Ready` condition, and converges on
its own when what it references arrives later.** Flux's health checks and `dependsOn` read
exactly these. A `MosquittoUser` applied before its Secret is `Ready=False` with a reason and
becomes `Ready=True` when the Secret appears, without a manual reconcile — the operator watches
what it references. A failure of one user never makes the broker or another user unready.

## Consequences

- The release serves one broker per `Mosquitto` honestly. A user who sets `replicas: 3` still
  gets three independent brokers, as today, and the README keeps saying so.
- R2 and R3 cost privilege: the operator reads the users' Secrets (ADR 0014 D7). That is the
  largest change to the security posture this release makes.
- R6 makes every future container a security-context decision first; a helper that needs root
  or a capability is not built.
- The HA work, when it comes, starts from research and seven open questions instead of from
  nothing — and from an API that did not promise anything it cannot keep.

## Alternatives Considered

- **Keep high availability in the first release.** It needs a product decision about the
  failover bound (HA3) that nobody can answer yet, and every option beyond a single broker buys
  a specific property at a large cost. Lost to the migration that is actually waiting.
- **Cap `replicas` at 1 now.** Honest, and free while there are no users; but the HA questions
  include what `replicas > 1` should mean (HA2), and deciding half of it now would pre-empt the
  answer. Lost; the README's statement stays the guard.
- **Relax pod security for helpers** (a root init container to fix file ownership, a sidecar
  with `CAP_KILL`). Not needed: the ownership is solved by a copy as uid `1883`
  ([broker-behaviour.md](../developer/broker-behaviour.md#m6--kubernetes-cannot-produce-a-file-mosquitto-will-accept-in-future-versions))
  and the signal by a shared uid. Lost.

## Residual risks

- Not verified: that Flux's health checks evaluate a custom resource's `Ready` condition the way
  D6 assumes. This rests on Flux's documentation, not on a run in this repository.
- ~~Not verified: that a sidecar running as uid `1883` with every capability dropped can signal
  the broker across `shareProcessNamespace` under PodSecurity `restricted`.~~ *(Measured 2026-10-05
  on Kind, [broker-behaviour.md](../developer/broker-behaviour.md) M22: it can; a container under
  another uid gets `Operation not permitted`.)*
- Kind is the only cluster this operator has been observed on (the E2E tier, first observed
  2026-10-05); D1's migration is the first production observation.

## References

- [docs/planning/project-plan.md](../planning/project-plan.md) — the phases of this release
- [docs/planning/ha-research.md](../planning/ha-research.md) — the parked research and HA1–HA7
- [ADR 0013](0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md),
  [ADR 0014](0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md),
  [ADR 0008](0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md) Group C
- [`internal/builder/statefulset.go`](../../internal/builder/statefulset.go) — the pod security context today
