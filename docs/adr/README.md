# Architecture Decision Records

Every durable architecture decision of this operator lives here, one file per decision
family. An ADR records **what was decided, why, what was rejected, and what it costs** — so a
later change can argue with the decision instead of rediscovering it.

## Format

Filename: `NNNN-kebab-case-title.md`, numbered in the order they were written.

Sections, in this order:

| Section | Content |
|---|---|
| `# ADR NNNN: Title` | The decision as a title, not a topic |
| `## Status` | `Accepted` / `Superseded by ADR NNNN` / `Amended`, plus `Date:` and what is actually implemented versus open |
| `## Context` | The forces and the concrete failure that made the decision necessary |
| `## Decision` | `D1 … Dn`, each a rule that holds going forward, in present tense |
| `## Consequences` | What this costs, including the parts nobody likes |
| `## Alternatives Considered` | Each option and why it lost |
| `## Residual risks` | Accepted risks, open items, and what was **not** verified |
| `## References` | Relative links to the code and to sibling ADRs |

Ground rules: English only; every claim verified against the code, with unverified statements
marked as such; identifiers (`functions`, `annotations`, constants) quoted exactly so the ADR
stays checkable against the tree.

One rule this repository leans on harder than most: **an ADR may record a decision whose code
does not exist yet, but it must say so in its own `Status`.** [ADR 0002](0002-the-metrics-exporter-is-written-here.md)
is such a record. A reader must never have to run `grep` to find out whether an ADR describes
the tree or a plan for it.

## Keeping them current

**An ADR is part of the code, not a historical note.** When a decision changes, the ADR is
updated in the same change — the `Decision` section states the new rule, the `Status` records
the amendment with its date, and the superseded rule is marked in place rather than deleted.
A reader must never find the old rule stated as current.

## Index

### Broker workload and configuration

| ADR | Decision |
|---|---|
| [0001](0001-the-operator-consumes-tls-material-it-never-issues-it.md) | The operator consumes TLS material, it never issues it |
| [0007](0007-one-broker-image-pin-and-why-not-the-openssl-tag.md) | One broker image pin, on `2.1.2-alpine`, and not on the `-openssl` tag |
| [0008](0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md) | The generated broker is anonymous, and `spec.config` can undo the rest |

### Reconciliation and privilege

| ADR | Decision |
|---|---|
| [0006](0006-both-install-paths-grant-the-same-authority.md) | Both install paths grant the same authority, and a test compares what they render |
| [0009](0009-delete-only-through-owner-references.md) | Delete only through owner references, and never patch |

### Observability

| ADR | Decision |
|---|---|
| [0002](0002-the-metrics-exporter-is-written-here.md) | The broker metrics exporter is written in this repository — **recorded ahead of the code; none of it is implemented** |

### Build, CI and verification

| ADR | Decision |
|---|---|
| [0003](0003-the-go-version-is-one-fact-in-four-files.md) | The Go version is one fact in four files, and one Renovate PR moves all four |
| [0004](0004-two-e2e-legs-and-no-version-matrix.md) | Two E2E legs, shaped by node count, and no version matrix |
| [0005](0005-fork-pull-requests-execute-on-the-self-hosted-runners.md) | Fork pull requests execute on the self-hosted runners, gated outside the repository |
| [0010](0010-a-check-is-not-a-check-until-it-has-failed-on-purpose.md) | A check is not a check until it has failed on purpose |

## Related documents

* [README.md](../../README.md) — user-facing reference: the naming conventions, the CRD and the
  two security-relevant modes ([ADR 0001](0001-the-operator-consumes-tls-material-it-never-issues-it.md),
  [ADR 0008](0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md))
* [SECURITY_ARCHITECTURE.md](../../SECURITY_ARCHITECTURE.md) — trust boundaries, the privilege
  footprint and the hardening checklist ([ADR 0006](0006-both-install-paths-grant-the-same-authority.md),
  [ADR 0009](0009-delete-only-through-owner-references.md))
* [DEVELOPER.md](../../DEVELOPER.md) — repository layout, the reconcile pipeline, and the
  build/test/CI matrix ([ADR 0003](0003-the-go-version-is-one-fact-in-four-files.md),
  [ADR 0004](0004-two-e2e-legs-and-no-version-matrix.md),
  [ADR 0010](0010-a-check-is-not-a-check-until-it-has-failed-on-purpose.md))
* [CLAUDE.md](../../CLAUDE.md) — project conventions and the obligation to update an ADR in the
  same change that changes the behaviour it describes
