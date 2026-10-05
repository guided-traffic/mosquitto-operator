# Mosquitto Operator

Repo: https://github.com/guided-traffic/mosquitto-operator
Module `github.com/guided-traffic/mosquitto-operator` · API group `mko.gtrfc.com`, version `v1` ·
Kind `Mosquitto`, resource `mosquittoes`, short name `mq`.
**Status: `v0.1.x` is released — one `Mosquitto` renders four objects and every broker is
anonymous. The next release is decided and not built: one broker run from Git through Flux, with
`MosquittoUser` objects, credentials in the users' own Secrets and changes that apply themselves
([ADR 0012](docs/adr/0012-the-first-release-is-one-broker-run-from-git-and-high-availability-is-parked.md)).
High availability is parked.** The order of the work is [the project plan](docs/planning/project-plan.md);
the work lists are in [docs/tickets/](docs/tickets/README.md).

**Nothing in this repository has ever been observed running against a real cluster.** Every
statement here is read out of the tree. The E2E suite exists, but its CI jobs in
[`release.yml`](.github/workflows/release.yml) have been commented out since 2026-09-01, and
`semantic-release` no longer waits for them; no run of the suite has been seen. Where that
matters, the ADRs say so in their own `Status` sections.

## Language policy

All code, comments, commit messages, documentation and CRD fields are written in **English**.
Conversation with the owner may be German.

## Where things are, and where a statement goes

A statement has exactly one home
([ADR 0011](docs/adr/0011-documentation-has-five-homes-and-tickets-are-work-lists-that-get-archived.md)):

| Kind | Home |
|---|---|
| A decision — what the operator does and why, what was rejected | an [ADR](docs/adr/README.md) |
| How the code works and how to contribute | [docs/developer/](docs/developer/README.md) — there is no `DEVELOPER.md` |
| What the pinned broker image actually does, measured | [docs/developer/broker-behaviour.md](docs/developer/broker-behaviour.md) |
| What somebody running the operator needs | [docs/operations/](docs/operations/README.md) |
| The threat model and the gap each mechanism leaves | [docs/security/](docs/security/README.md), one page per perspective, each ending with `## What this does not cover`; reporting is [SECURITY.md](SECURITY.md); there is no `SECURITY_ARCHITECTURE.md` |
| Work still outstanding | a [ticket](docs/tickets/README.md), archived when the work lands |
| The reference tables (CRD fields, Helm values, operator flags, deterministic names) | [README.md](README.md) and nowhere else |
| An open decision | the `## Open questions` section of a [ticket](docs/tickets/README.md) |
| The plan and the parked HA research | [docs/planning/](docs/planning/) — transitional, consumed into ADRs and tickets |

**Read the page for a subsystem before you change it, and update it in the same change.**

## Decisions live in ADRs; tickets are work lists that get archived

- **Every durable decision is an ADR** in the format of [docs/adr/README.md](docs/adr/README.md),
  written in the session the decision is taken; a new ADR gets its row in the index in the same
  change. Changing behaviour an ADR describes means updating that ADR in the same change. **A
  decision that changes an existing record is an amendment of that record, never a new ADR**: its
  `Decision` states the new rule, its `Status` records the amendment with the date, the old rule
  is marked in place. A rule decided but not built says so where it stands.
- **A ticket is a work list and nothing else:** `docs/tickets/NNN-<slug>.md`, frontmatter per the
  rules page, current state only (no History, no strike-throughs, no dated annotations), closed by
  extraction then moved to `docs/tickets/archive/`. A finding goes into an existing ticket first.
  A number is never reused.
- **An open security finding is embargoed:** `security: live|boundary` unfixed → the file is
  `local_NNN-<slug>.md` (gitignored by this repository's `.gitignore`), and no tracked file,
  commit or PR carries its details or its file name.
- **Nothing outside `docs/tickets/` cites a ticket** — not by number, label, path or file name.
  Cite the ADR.
- **A phase of the plan becomes tickets when it starts**, in a session dedicated to that
  conversion: a family ticket and its children.

## Open decisions are worked one question at a time

The founding question catalog is consumed ([docs/planning/questions.md](docs/planning/questions.md)
is a tombstone). A new open decision lives in a ticket's `## Open questions` section. Present
**one** question per turn to the owner, with the options researched against this tree — and
measured against the pinned image where broker behaviour decides it — and the recommended one
justified. An answered question becomes an ADR or an amendment in the same session. Do not build
on an unanswered question.

## What this operator does — and what it is not

One `Mosquitto` produces exactly four objects today: a ConfigMap holding the generated
`mosquitto.conf`, a headless Service, a ClusterIP client Service and a StatefulSet of broker pods
(`reconcileResources` in [`internal/controller/mosquitto_controller.go`](internal/controller/mosquitto_controller.go)).
The fields, the names it derives and the generated file are in the [README](README.md)
reference.

**The broker pods are independent Mosquitto processes behind one Service.** No bridging, no shared
sessions, no shared retained messages, no clustering. Raising `spec.replicas` buys process
redundancy, not a highly available broker — a subscriber on one pod never sees a retained message
published through another. High availability is parked
([docs/planning/ha-research.md](docs/planning/ha-research.md)); do not write a comment, doc line
or commit message that implies otherwise, and build nothing on `replicas > 1`.

**Not in the tree, and not to be documented as if it were:** `MosquittoUser`, authentication,
ACLs, the reload sidecar, the `secrets` grant (all decided in ADR 0013, ADR 0014 and ADR 0008
Group C, none built), the metrics exporter (ADR 0002, nothing built), PodDisruptionBudgets,
NetworkPolicies (deliberately never shipped, ADR 0008 D16), admission webhooks, ServiceMonitor,
PrometheusRule, and any cert-manager dependency at any layer.

## Reconcile rules that are easy to break

- **`ensureOwned` before every write onto a generated name.** `metav1.IsControlledBy` or refuse; a
  label is not a proof. The refusal is a reconcile failure (`phase: Failed`, `Ready=False`, reason
  `ReconcileFailed`). A new managed kind inherits this, not an exemption
  ([ADR 0009](docs/adr/0009-delete-only-through-owner-references.md)).
- **No `delete` and no `patch` in the ClusterRole**, and identical authority on both install
  paths ([ADR 0006](docs/adr/0006-both-install-paths-grant-the-same-authority.md)). A
  `Mosquitto` carrying a `DeletionTimestamp` gets no writes at all; teardown is the garbage
  collector's. **A new RBAC marker updates the chart's hand-written `clusterrole.yaml` in the same
  change**; `make verify-rbac-parity` catches the drift.
- **`StatefulSetHasChanged` compares replicas, object labels, template labels and the two hash
  annotations — never the pod spec structurally**, because the API server defaults pod fields and
  a structural comparison loops forever. A new field outside the pod template is not picked up by
  the hash.
- **`volumeClaimTemplates` are written on create and never updated.**
- **Config changes need the config hash.** Mosquitto reads its file once at start; a ConfigMap
  update restarts nothing, so `mko.gtrfc.com/config-hash` carries a config change into a roll.
- **Every container the operator renders meets the restricted Pod Security Standard and runs as
  uid `1883`** ([ADR 0012](docs/adr/0012-the-first-release-is-one-broker-run-from-git-and-high-availability-is-parked.md) D4).
- **`ExtractVersionFromImage` always returns a valid label value**, asserted against
  `validation.IsValidLabelValue` and never against expected strings.

How the reconcile works end to end: [docs/developer/architecture.md](docs/developer/architecture.md).

## Stack facts

- The broker image pin `eclipse-mosquitto:2.1.2-alpine` lives in exactly two constants held equal
  by a test — `builder.DefaultImage` and `testimages.MosquittoImage` — and moves by one Renovate
  `customManager`, capped `<3`. **Do not copy the pin anywhere else**
  ([ADR 0007](docs/adr/0007-one-broker-image-pin-and-why-not-the-openssl-tag.md)).
- The Go version is one fact in four files, moved together by Renovate; a new place that names it
  arrives with its own `customManager` in the same change
  ([ADR 0003](docs/adr/0003-the-go-version-is-one-fact-in-four-files.md)).
- TLS material reaches a broker only through `spec.tls.secretName`, a Secret the operator never
  creates, renews or reads ([ADR 0001](docs/adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md)).
- Generated configuration uses only what Mosquitto 2.1 accepts and 3.0 is announced to keep —
  plugins, never `password_file`/`acl_file`/`per_listener_settings`.
- Two install paths: Helm (`deploy/helm/mosquitto-operator`) and kustomize
  (`kustomize build config/default`); only the kustomize RBAC is generated.

## Testing

**The Makefile is the entry point**, for CI and for you. Tools resolve to `./bin`, never to
`PATH`. The tiers, what each can answer and what it needs: [docs/developer/testing.md](docs/developer/testing.md);
the full target matrix: [docs/developer/build-test-lint.md](docs/developer/build-test-lint.md).

| Tier | Target |
|---|---|
| Unit | `make test-unit` (`make test-unit-coverage`) |
| Integration (envtest: an API server, no kubelet, no garbage collection) | `make test-integration` |
| E2E (Kind + Helm-installed operator) | `make test-e2e`, or `make e2e-local` with the cluster |
| Pinned-image tools | `make test-image-tools` |
| RBAC parity | `make verify-rbac-parity` |
| Renovate managers | `make verify-ci-references` |
| Lint, security, complexity | `make lint`, `make gosec`, `make vuln`, `make cyclo` (threshold 15) |
| Generated artifacts | `make generate-all` |

No `-short`, no `testing.Short()`: no test can remove itself from CI while passing locally. A fix
comes with the test that failed without it, in the cheapest tier that can answer the question —
never in one that cannot. **A check is not a check until it has failed on purpose**, with the
exact message recorded ([ADR 0010](docs/adr/0010-a-check-is-not-a-check-until-it-has-failed-on-purpose.md)).

# Working rules

- Keep cyclomatic complexity under 15 for every function; `make cyclo` is the gate.
- Run `make lint` and the relevant test target before reporting a task done.
- Run `make generate-all` after any change under `api/v1/` or to an RBAC marker, and commit the
  result: `generated-manifests` fails on a dirty tree.
- **Do not commit, add or push to git.** Report a conventional commit message and let the owner
  review and commit. No apostrophe anywhere in a commit message; no `BREAKING CHANGE` and no `!`
  — the project stays on 0.x.
- Never develop across repositories unprompted; a needed change elsewhere is a
  `local_<ticketname>.md` ticket here.
- Temporary files go into the local `tmp/` folder in this repository, never into the system `/tmp`.
- Scrutinise security-relevant requests: name the risk, propose the safer alternative, discuss,
  then implement and document the tradeoff if the owner accepts it.
- Separate verified from unverified in every report, and never let an assumption travel as a fact.
  The planning documents describe intent; the code and the ADRs describe fact. When they disagree,
  the code wins and the disagreement is named.
