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

Written 2026-10-05, last brought up to date on 2026-11-12. Phases, not dates; the order is the
order of the work, and each phase is releasable on its own.

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
Phase 7, the exporter, was built on 2026-10-05 and closed on 2026-11-11, when its four open
decisions were answered: the gate, the port, the TLS pin and the series names, three of which were
renamed ([ADR 0002](../adr/0002-the-metrics-exporter-is-written-here.md) D3, D6 and D10 as amended;
[metrics.md](../operations/metrics.md)).
The upkeep finding that Renovate could not look up the Trivy action was answered and built on
2026-11-09: Trivy runs unpinned from `aquasec/trivy:latest`
([ADR 0005](../adr/0005-fork-pull-requests-execute-on-the-self-hosted-runners.md) D9).

## Phase 6 — The Home Assistant migration

**Goal:** the release does what ADR 0012 D1 says, on a real cluster — the first observation of
this operator running anywhere but CI.

**Effort:** S, plus the owner's time.

- *(Built 2026-10-05: the README Flux example, step 4 of the fast start, run on Kind with Flux
  `v2.8.6` from an `OCIRepository` — [flux.md](../operations/flux.md). Learned there: plain
  `healthChecks` pass before `Ready`, so the example uses `healthCheckExprs` with `wait: true`.)*
- *(Inventory 2026-11-12, in the owner's Flux repository.)* The existing broker serves Home
  Assistant, AppDaemon, two fleets of devices (ESPresense nodes, Tasmota plugs), an ebusd heating
  bridge and, through a bridge, two SBFspot pollers; there is no Zigbee2MQTT. Two findings bear on
  this operator, neither needs a change of it:
  - **Each fleet shares one login.** Under `use_username_as_clientid` (ADR 0008 D14) the devices of
    a fleet would take each other's session in a loop. Since every client gets new credentials
    anyway, every device gets a `MosquittoUser` of its own; D14 stays as decided.
  - **The devices need a LAN address.** The operator renders no LoadBalancer (ADR 0008 D16); the
    owner's repository carries its own `LoadBalancer` Service against the selector labels.
- *(Built and run 2026-11-12.)* The operator installed by Flux from the published chart `0.2.18`
  with `secretAccess.mode: namespaces` and `secretSecurity: true`; a second broker beside the
  existing one, with one user and one SOPS Secret per client and ACLs derived from the observed
  traffic; a script in the owner's repository that moves one client at a time and waits for its
  login. Every client that was online moved the same day — Home Assistant, AppDaemon, eleven
  ESPresense nodes, nine Tasmota plugs, an ebusd bridge on MQTT 3.1, two SBFspot pollers and an
  exporter — and the broker's own exporter is scraped by Prometheus. Observed on the way:
  - **A user added to a running broker is refused for about 90 seconds** — the kubelet's Secret
    refresh plus the reloader, as [users.md](../operations/users.md) states; clients retry.
  - **Through the relay, the old broker's will can overtake a moved device:** a device that reboots
    without DISCONNECT is back on the new broker before the old one fires its retained `offline`,
    which the relay then copies over. Restarting the device once more after its old session is gone
    fixes it. A property of running two brokers through a relay, not of this operator.
- *(Decided by the owner 2026-11-12, recorded in the owner's repository.)* A temporary relay — a
  plain Mosquitto with two `topic # both` bridges, outside this operator — keeps both brokers in
  sync while clients move one at a time. Measured locally on 2026-11-12 with the pinned image:
  every message arrived exactly once on both sides, retained values in both directions; a
  restarted broker replays its persisted retained values to the other side, which can revert a
  value changed during the restart. Hence Home Assistant and AppDaemon move first, so a broker
  roll by an operator release can only stale the old broker. Nothing in this operator changes.
- *(Done 2026-11-12.)* The two devices that were offline moved when they came back; the relay was
  switched off and the old broker removed, after no client but the relay used it.
- **Left:** a password rotated in Git, observed reaching the broker; and what was run and observed
  written into [docs/operations/](../operations/README.md) as a page of its own.
- **Done when** every client runs against it, a password rotated in Git reaches the broker without
  a manual step (and the in-cluster clients that mount their Secret; a device keeps its password
  in its own settings), and what was run and observed is written into
  [docs/operations/](../operations/README.md).

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
