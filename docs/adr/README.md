# Architecture Decision Records

Every durable decision of this operator lives here, one file per decision family. An ADR
records **what was decided, why, what was rejected, and what it costs** — so a later change can
argue with the decision instead of rediscovering it.

## Format

Filename: `NNNN-kebab-case-title.md`, numbered in the order they were written.

Sections, in this order:

| Section | Content |
|---|---|
| `# ADR NNNN: Title` | The decision as a title, not a topic |
| `## Status` | `Accepted`, or `Accepted, amended <date> (…)`, plus `Date:` and what is actually implemented versus open |
| `## Context` | The forces and the concrete failure that made the decision necessary |
| `## Decision` | `D1 … Dn`, each a rule that holds going forward, in present tense |
| `## Consequences` | What this costs, including the parts nobody likes |
| `## Alternatives Considered` | Each option and why it lost |
| `## Residual risks` | Accepted risks, open items, and what was **not** verified |
| `## References` | Relative links to the code and to sibling ADRs |

Ground rules: English only; every claim verified against the code, with unverified statements
marked as such; identifiers (`functions`, `annotations`, constants) quoted exactly so the ADR
stays checkable against the tree. An ADR may link into the code and into `docs/`, and it cites no
ticket — the rule is [ADR 0011](0011-documentation-has-five-homes-and-tickets-are-work-lists-that-get-archived.md).

One rule this repository leans on harder than most: **an ADR may record a decision whose code
does not exist yet, but it must say so in its own `Status`**, and a rule that is decided but not
built is marked "decided, not built" where it stands. A reader must never have to run `grep` to
find out whether an ADR describes the tree or a plan for it.

## Keeping them current

**An ADR is part of the code, not a historical note.** When a decision changes, the ADR is
updated in the same change — the `Decision` section states the new rule, the `Status` records the
amendment with its date, and the superseded rule is marked in place rather than deleted. A reader
must never find the old rule stated as current.

**A decision that changes an existing record is an amendment of that record, never a new one.**
No ADR amends, supersedes, overrides or invalidates another; when an answer touches several
records, each of them is amended in place and says why. A new ADR is written only for a decision
no existing record covers.

The record's row in the index below changes in the same change as its `Status`: a new record gets
its row, with its *State*, in the change that writes it, and an amendment that moves what is
built, or supersedes a rule, updates the row's *State* with it.

## How a decision gets here

The founding question catalog was worked one question per turn and closed on 2026-10-05 into
ADR 0012 to ADR 0014 and amendments of ADR 0001, 0002, 0006, 0007 and 0008
([ADR 0011](0011-documentation-has-five-homes-and-tickets-are-work-lists-that-get-archived.md) D10).
From here on an open decision lives in the step of [the project plan](../planning/project-plan.md)
that needs it (ADR 0011 D12), is put to the owner one at a time with its options and a
recommendation, and **an answered question becomes an amendment of the record it
changes, or a new ADR when no record covers it, in the same session**.

## Index

Every record here is **Accepted**. The *State* column is the coarse build state as of 2026-10-05:
**Implemented**, **Partly built** (some rules hold in the tree, the rest are decided and
outstanding) or **Not built** (decided, nothing of it exists yet). The record's own `Status` is
the detail.

### Scope and documentation

| ADR | Decision | State |
|---|---|---|
| [0011](0011-documentation-has-five-homes-and-tickets-are-work-lists-that-get-archived.md) | Documentation has five homes, tickets are work lists that get archived, an open security finding is embargoed, and planning documents are consumed | Implemented |
| [0012](0012-the-first-release-is-one-broker-run-from-git-and-high-availability-is-parked.md) | The first release is one broker run from Git, and high availability is parked | Partly built — R1, R2, R4, R5, R6 and R3 for users; R3 for certificates and D1's migration not yet |

### Broker workload and configuration

| ADR | Decision | State |
|---|---|---|
| [0001](0001-the-operator-consumes-tls-material-it-never-issues-it.md) | The operator consumes TLS material, it never issues it — and a renewed certificate is reloaded in place | Partly built — D1–D9 implemented, D1 and D6 as amended; D10 (in-pod reload) not built |
| [0007](0007-one-broker-image-pin-and-why-not-the-openssl-tag.md) | One broker image pin, on `2.1.2-alpine`, and not on the `-openssl` tag | Implemented — D9, D10 built 2026-10-05 |
| [0008](0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md) | The generated broker is anonymous, and `spec.config` can undo the rest — amended: never anonymous, takeover-safe, `spec.config` allowlisted, no NetworkPolicy | Implemented — Group C (D13–D16) built 2026-10-05; D1, D2, D6, D7, D9 superseded |

### Users and credentials

| ADR | Decision | State |
|---|---|---|
| [0013](0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md) | A client is a `MosquittoUser` with its credentials in its own Secret | Implemented — 2026-10-05 |
| [0014](0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md) | Credentials reach the broker as one rendered Secret and a signal, never as a restart | Partly built — D1–D4, D6–D8, D10; D5 (TLS reload) not built, D9 later by decision |

### Reconciliation and privilege

| ADR | Decision | State |
|---|---|---|
| [0006](0006-both-install-paths-grant-the-same-authority.md) | Both install paths grant the same authority, and a test compares what they render | Implemented — D9 built 2026-10-05 |
| [0009](0009-delete-only-through-owner-references.md) | Delete only through owner references, and never patch — and an update keeps foreign labels | Implemented — D9 built 2026-10-05, the removal of a deleted `spec.podLabels` key on an open question |

### Observability

| ADR | Decision | State |
|---|---|---|
| [0002](0002-the-metrics-exporter-is-written-here.md) | The broker metrics exporter is written in this repository, and logs in as a reserved user | Not built — only D8, the operator's own endpoint, describes existing code |

### Build, CI and verification

| ADR | Decision | State |
|---|---|---|
| [0003](0003-the-go-version-is-one-fact-in-four-files.md) | The Go version is one fact in four files, and one Renovate PR moves all four | Implemented |
| [0004](0004-two-e2e-legs-and-no-version-matrix.md) | Two E2E legs, shaped by node count, and no version matrix | Implemented — out of CI from 2026-09-01 to 2026-10-05, restored |
| [0005](0005-fork-pull-requests-execute-on-the-self-hosted-runners.md) | Fork pull requests execute on the self-hosted runners, gated outside the repository | Implemented — the gate is a GitHub setting, not verifiable from the tree |
| [0010](0010-a-check-is-not-a-check-until-it-has-failed-on-purpose.md) | A check is not a check until it has failed on purpose | Implemented |
