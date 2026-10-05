# Project plan

How `v0.1.x` becomes the operator the Home Assistant migration needs: one broker per `Mosquitto`,
run from Git through Flux, with users, permissions and credentials that follow every change on
their own ([ADR 0012](../adr/0012-the-first-release-is-one-broker-run-from-git-and-high-availability-is-parked.md)).

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

Built for the next release on 2026-10-05, and deleted here: phase 3 (the measurements,
[broker-behaviour.md](../developer/broker-behaviour.md) M19–M27) and phase 5 (a renewed
certificate reloaded in place, [ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md)
D10). Phases 1, 2 and 4 keep only what is left of them.

## Phase 1 — The tree tells the truth, and the release gate is back

**Built 2026-10-05.** The E2E tier gates releases again (first CI run of both legs: `Test and
Release` run `37367556237`), the statements the code contradicted are corrected, the pipeline holds
only the authority it uses, and the ownership refusal message is pinned. What it built is recorded
in ADR 0001, 0003, 0004, 0005, 0007, 0009 and 0010 and in
[ci-and-release.md](../developer/ci-and-release.md). One check is left, because only a merge to
`main` can run it:

- **A release run on `main` with the reduced permissions.** `semantic-release` now holds
  `contents: read` only and writes through the app token; `build` logs out of Docker Hub before the
  pinned SBOM and release actions. **Done when** the first `Test and Release` run on `main` after
  the merge succeeds through `semantic-release`, and the `Release Docker & Helm` run it triggers
  succeeds through `release-helm-gh`. A step that fails for a permission gets the permission back,
  with a comment naming the call that needed it, and ADR 0005 D6 says so.

## Phase 2 — Pod labels, the configuration gate, the admission guard, and the first Secret switch

**Built 2026-10-05**: `spec.podLabels` and `spec.podAnnotations` (ADR 0012 D5), updates that merge
(ADR 0009 D9), the `config-check` init container (ADR 0007 D10, with the measurement M19 it
needed), the PodSecurity admission guard (ADR 0012 D4) and `secretSecurity` for the TLS Secret
(ADR 0014 D10). One decision is open, and the tree is built on its recommended answer:

### 2.1a — What happens to a key deleted from `spec.podLabels` or `spec.podAnnotations`?

ADR 0009 D9 makes every update merge the operator's keys over the live labels and annotations and
keep every other key; its accepted cost is that "a label the operator once set and later stops
setting is never removed by merging". `spec.podLabels` turns that cost into a user-visible one: a
pod label deleted from the `Mosquitto` in Git would stay on the pods forever, and a label a
NetworkPolicy or a scraper selects on keeps granting what it granted.

- **A — The StatefulSet records the keys it applied and removes those that left the spec.**
  *(Recommended, and built.)* Two annotations on the StatefulSet object,
  `mko.gtrfc.com/applied-pod-labels` and `mko.gtrfc.com/applied-pod-annotations`, list the sorted
  keys last written; `builder.MergeStatefulSet` deletes exactly the keys that one lists and the new
  spec does not, then merges. Cost: two annotations of bookkeeping, and a foreign label that
  happens to share a removed key is removed too. It is what `kubectl apply` does with its
  last-applied record, scoped to the two maps the user owns, and it needs no status as memory.
- **B — Accept D9's cost for these keys as well.** No bookkeeping; removing a pod label means
  editing the StatefulSet's pod template by hand, which the next roll keeps. Against R4 of ADR
  0012, half-met: a label reaches the pods, it never leaves.
- **C — Replace the pod template's labels and annotations wholesale, keep merging elsewhere.**
  Simple, and it brings back what D9 removed: `kubectl.kubernetes.io/restartedAt` and every
  policy-engine label on the template disappear on any write.

A is recommended because it is the only option under which a Git change to `spec.podLabels` means
the same thing in both directions, at the cost of state the operator already owns. The answer
becomes an amendment of ADR 0009 D9.

**Answer:** _open_

## Phase 4 — Users, permissions, and a broker that requires a login

**Built 2026-10-05**: the `MosquittoUser` kind, the renderer, `<name>-auth`, the pod with
`auth-init` and `reloader`, the generated listener that requires a login, the `spec.config`
allowlist, the watches and statuses, and the Secret grant in two modes on both install paths
(ADR 0006 D9, ADR 0008 D13–D16, ADR 0013, ADR 0014 D1–D4, D6–D8, D10). The scenario of 4.8 passed on
Kind (`TestE2E_Users_TheBrokerFollowsItsUsers`). One decision is open, and the tree is built on its
recommended answer:

### 4.6a — Is a Secret cache without data what ADR 0014 D10 asks for, instead of one restricted to labelled Secrets?

ADR 0014 D10 says that with `secretSecurity: true` "the operator's Secret cache is restricted to
labelled Secrets". Building it showed that a label-restricted cache cannot hold the operator's own
`<name>-auth`, which the `Owns` watch and the ownership check read, unless the operator labels its
own Secret as consenting — which would let any `Mosquitto` or `MosquittoUser` of the namespace name
it. What the label restriction is for is that the operator never holds or reads the data of a
Secret whose owner did not consent.

- **A — Cache every Secret in scope stripped of its data, check the label on the cached metadata,
  read a Secret's data with one uncached `get` only after the check.** *(Recommended, and built:
  `controller.StripSecret`, `readCredentials`.)* No Secret data is in the cache at all, with or
  without `secretSecurity`, and annotations — where `kubectl apply` records a whole Secret — are
  stripped too. Cost: the names and labels of every Secret in scope are in memory, and one API
  request per user per pass reads the data.
- **B — Restrict the cache to labelled Secrets, and label `<name>-auth` consenting.** Matches the
  text; the cache holds the full data of every labelled Secret, and every `Mosquitto` of the
  namespace may then name a broker's rendered hashes as its TLS Secret or a user's credentials.
- **C — Two caches: labelled Secrets, and Secrets owned by a `Mosquitto`.** Matches the text
  without labelling `<name>-auth`, at the cost of a second informer setup controller-runtime does
  not offer for one type without a custom cache; and the labelled Secrets' data is still cached.

A is recommended because it is stricter than the text — no Secret data in the cache in either
setting — and costs one request per user per pass. The answer becomes an amendment of ADR 0014
D10.

**Answer:** _open_

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

**Goal:** broker metrics for Prometheus.

**Builds:** [ADR 0002](../adr/0002-the-metrics-exporter-is-written-here.md) as amended — the
reserved `mko-exporter` user with `read $SYS/#`, its password a key of `<name>-auth`.

**Effort:** M.

- The steps ADR 0002 names; the renderer of 4.2 renders the reserved user; the exporter container
  gets the same security context as every other container.
- Tests: as ADR 0002 names them, plus an E2E test that the exporter logs in as `mko-exporter` and
  that no `MosquittoUser` can claim the name.

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
