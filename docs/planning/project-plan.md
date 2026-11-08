# Project plan

What is left between the released operator and the one the Home Assistant migration needs: one
broker per `Mosquitto`, run from Git through Flux, with users, permissions and credentials that
follow every change on their own
([ADR 0012](../adr/0012-the-first-release-is-one-broker-run-from-git-and-high-availability-is-parked.md)).
The operator side was released in `v0.2.0` on 2026-10-06.

**This file is the work list.** Each phase is worked out to the step: what it builds, which files
and functions it touches, which tests in which tier prove it, which documentation and records it
updates, and when it is done. Phases are built from here directly, not converted into tickets; a
finding goes into the phase that does the work; an open decision goes into the step that needs it
and becomes a record when it is answered. A phase that is done is deleted from this file
([ADR 0011](../adr/0011-documentation-has-five-homes-and-tickets-are-work-lists-that-get-archived.md)
D10, D12). The only tickets are embargoed security findings, which a tracked file cannot carry.

Written 2026-10-05. Phases, not dates; the order is the order of the work, and each phase is
releasable on its own.

## Working agreements

- **Decide before building.** Every decision a step needs is a record already. A question that
  comes up while building is written into the step with its options and put to the owner one at a
  time; the answer becomes a record or an amendment in the same session. Code written on an
  undecided question is speculation.
- **Measure before relying.** A step whose design rests on broker or kubelet behaviour starts with
  the measurement it names; the result goes into
  [docs/developer/broker-behaviour.md](../developer/broker-behaviour.md) with command and output,
  and a result that contradicts a record stops the step until the record is amended.
- **Tests in the cheapest tier that can answer the question**, never in one that cannot — envtest
  starts no pod and collects no garbage. Every new guard is observed failing on purpose first,
  with the message recorded ([ADR 0010](../adr/0010-a-check-is-not-a-check-until-it-has-failed-on-purpose.md)).
- **A step is done with its documentation**: the README reference for every field, value and flag
  it adds, the page under `docs/operations/`, `docs/security/` or `docs/developer/` that describes
  what it built, and the `Status` and index row of every record it builds.
- **The Makefile is the entry point.** `make lint`, `make cyclo` (threshold 15), the test target of
  the tier, and `make generate-all` after any change under `api/v1/` or to an RBAC marker — with
  the generated files committed.
- **The project stays on 0.x.** No commit carries `BREAKING CHANGE` or `!`, and no commit message
  carries an apostrophe.
- **Verification is named.** "Done" means what was run, against what, with what result. The E2E
  tier runs on Kind, locally and in CI; what it cannot show is a production cluster, which phase 6
  is the first to reach.

Proposed names below — chart values, flags, labels, reason strings, new packages — are proposals;
a name that becomes API surface is fixed in the step that builds it and written into the README
reference.

## Done

`v0.1.0` to `v0.1.8`: one `Mosquitto` renders a ConfigMap, a headless and a client Service and a
StatefulSet; TLS from an existing Secret; anonymous brokers; two install paths with equal
authority; five test tiers. What it built is in the records 0001 to 0010 and in
[docs/developer/](../developer/README.md).

Built on 2026-10-05, released in `v0.2.0` on 2026-10-06, and deleted here:

- phase 1, the tree tells the truth and the release gate is back — the E2E gate, the corrected
  statements, the reduced pipeline authority (observed in the `v0.2.0` release, ADR 0005), the
  pinned refusal message (ADR 0009 D5);
- phase 2, pod labels, the configuration gate, the admission guard and `secretSecurity` for the TLS
  Secret (ADR 0007 D10, ADR 0009 D9 as amended on 2026-11-08, ADR 0012 D4 and D5, ADR 0014 D10);
- phase 3, the measurements ([broker-behaviour.md](../developer/broker-behaviour.md) M19–M27);
- phase 4, users, permissions and a broker that requires a login (ADR 0006 D9, ADR 0008 D13–D16,
  ADR 0013, ADR 0014 D1–D4, D6–D8 and D10 as amended on 2026-11-08);
- phase 5, a renewed certificate reloaded in place
  ([ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md) D10).

Phases 1, 2 and 4 were closed on 2026-11-08, after their last two open decisions were answered.

## Phase 6 — The Home Assistant migration

**Goal:** the release does what ADR 0012 D1 says, on a real cluster — the first observation of
this operator running anywhere but CI.

**Effort:** S, plus the owner's time.

- *(Built 2026-10-05: the README Flux example, step 4 of the fast start, run on Kind with Flux
  `v2.8.6` from an `OCIRepository` — [flux.md](../operations/flux.md). Learned there: plain
  `healthChecks` pass before `Ready`, so the example uses `healthCheckExprs` with `wait: true`.)*
- The owner migrates Home Assistant and Zigbee2MQTT onto the new broker through Flux.
- **Done when** both clients run against it, a password rotated in Git reaches both without a
  manual step, and what was run and observed is written into
  [docs/operations/](../operations/README.md).

## Phase 7 — The exporter

**Built 2026-10-05**: `cmd/exporter` and `internal/exporter`, the `exporter` container, the
reserved user `mko-exporter` with its generated password in `<name>-auth`, the stated
`sys_interval` and `max_packet_size`, and the tests of every tier — the E2E scenario passed on Kind
(`TestE2E_Metrics_TheExporterServesTheBrokersSysTree`). ADR 0002 left four points open; each is
built on its recommended answer, and each answer becomes an amendment of ADR 0002:

### 7a — What does the gate look like?

ADR 0002 D6 fixes the gate (absent means no container) and deliberately not the field.

- **A — `spec.metrics.enabled: bool`, default `false`.** *(Recommended, and built.)* Reads as what
  it does in a Git diff, and leaves room for more fields under `metrics` without a breaking
  change.
- **B — the presence of `spec.metrics: {}` turns it on.** One line shorter; an empty object as a
  switch is easy to misread and impossible to turn off without deleting the key.
- **C — A with the port and the container resources as fields now.** More API than any known need.

**Answer:** _open_

### 7b — How does a scraper reach the exporter, and does it authenticate?

- **A — a container port `9234` named `metrics`, on no Service, plain HTTP without
  authentication.** *(Recommended, and built.)* Per-pod is what `$SYS` is (D3); a `PodMonitor` or
  pod discovery finds it; no Service puts the endpoint behind a name. The endpoint is open to the
  cluster network ([H-20](../security/trust-boundaries.md#h-20)).
- **B — A, plus a `metrics` port on the headless Service** for `ServiceMonitor` users. A second
  way to reach the same endpoint, and an object change for every broker.
- **C — authentication with `TokenReview`/`SubjectAccessReview`.** Needs API access and a
  ServiceAccount token in the broker pod, which the pod deliberately has not (ADR 0002 D4) — the
  same reason the operator's own endpoint is open (D8).

**Answer:** _open_

### 7c — Under TLS, what does the exporter verify?

The only listener is `8883`, and its certificate names the broker's DNS names, not `127.0.0.1`.

- **A — accept exactly the certificate mounted at `/mosquitto/tls/tls.crt`, re-read on every
  handshake.** *(Recommended, and built.)* Works for every Secret shape, a hand-made one without
  `ca.crt` included, and still proves the peer holds the broker's key; the connection never
  leaves the pod.
- **B — verify the chain against `ca.crt` and a DNS name from the certificate.** Needs a `ca.crt`
  the `kubectl create secret tls` path does not write.
- **C — a second, plain listener on `127.0.0.1` for the exporter.** Changes the generated
  configuration and breaks "one listener" for a monitoring convenience.

**Answer:** _open_

### 7d — What are the series called?

- **A — a fixed table of the measured topics, `mosquitto_` names, counters with `_total` for what
  counts since the broker started, the load averages as one series per kind with a `window`
  label, the version as `mosquitto_version_info{version}`.** *(Recommended, and built.)* Unknown
  topics are skipped, missing ones absent (D1's consequence).
- **B — every numeric topic as a gauge named after its path.** No table to maintain; no counter
  semantics, and the names change whenever the broker adds a topic.

**Answer:** _open_

## Phase 8 — High availability

**Last, by the owner's decision:** started only when phases 1 to 7 are done
([ADR 0012](../adr/0012-the-first-release-is-one-broker-run-from-git-and-high-availability-is-parked.md)
D3). Its research and its questions are in [ha-research.md](ha-research.md); the questions
HA1–HA7 are put to the owner when this phase starts, not before, and the phase is worked out here
then.

## Later, when a need arrives

| What | Decided in | Built when |
|---|---|---|
| `MosquittoRole` | [ADR 0013](../adr/0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md) D7 | many users share identical ACLs |
| The dynamic-security mode | [ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md) D9 | ACL priorities are needed, or a client with a valid credential must be kickable |

## What is deliberately not planned

- Issuing certificates; cert-manager as a dependency at any layer
  ([ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md)).
- Admission webhooks and conversion webhooks — each needs a serving certificate.
- A NetworkPolicy shipped by the operator ([ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md) D16).
- An image policy in the operator ([ADR 0007](../adr/0007-one-broker-image-pin-and-why-not-the-openssl-tag.md) D9).
- Generating client passwords; the users own their Secrets.
- References across namespaces. A `Mosquitto`, its users, their Secrets and their ACLs share one
  namespace; no reference carries a namespace field
  ([ADR 0013](../adr/0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md) D1).
  The operator itself acts cluster-wide.
