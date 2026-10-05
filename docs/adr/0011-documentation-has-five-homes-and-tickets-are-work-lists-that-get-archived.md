# ADR 0011: Documentation Has Five Homes, Tickets Are Work Lists That Get Archived, an Open Security Finding Is Embargoed, and Planning Documents Are Consumed

## Status

Accepted. Date: 2026-10-05. The owner asked for the documentation of this repository to be
moved onto the format of a sibling project, whose rules were decided there between 2026-09-26
and 2026-10-01; this record adopts them for this repository, with the planning rule (D10) taken
from the same project's record on consuming a question catalog.

**Implemented** in the change that wrote this record:

- the root `DEVELOPER.md` moved into [docs/developer/](../developer/README.md), the root
  `SECURITY_ARCHITECTURE.md` into [docs/security/](../security/README.md) with
  [SECURITY.md](../../SECURITY.md) at the root for reporting only, and
  [docs/operations/](../operations/README.md) was created from what the README explained;
- the three bootstrap documents under `docs/tickets/` — a question catalog, a plan and an HA
  research note — moved into [docs/planning/](../planning/): every answered question became a
  record or an amendment in place, the catalog is a tombstone, the plan is
  [project-plan.md](../planning/project-plan.md);
- [docs/tickets/README.md](../tickets/README.md) carries the ticket rules, and
  [`.gitignore`](../../.gitignore) carries `docs/tickets/**/local_*`. Verified 2026-10-05:
  `git check-ignore -v docs/tickets/local_001-x.md docs/tickets/archive/local_002-y.md` names
  `.gitignore:55` for both — the repository's own file, not a global excludes file — and
  `docs/tickets/001-x.md` is not ignored.

The restructuring filed what it found — statements the code contradicts, and security findings,
the unfixed live or boundary ones under the embargo of D7 — as the first tickets. The first phase
of the plan becomes tickets when it starts (D10).

## Context

Until this change the repository kept its documentation in three root files — `README.md`,
`DEVELOPER.md`, `SECURITY_ARCHITECTURE.md` — and its planning in `docs/tickets/`, where a
question catalog, a plan and a research note had grown to roughly 1,300 lines in one session of
decisions. Those three planning files mixed answered questions, measurements, struck-through
recommendations and dated annotations: a reader could no longer tell which statement was the
current rule. A security document cited by section number, a developer document of 747 lines and
a planning directory that was also called "tickets" are the shape the sibling project moved away
from, for reasons that apply here unchanged: a document nobody finishes is a document nobody
checks, and a file that mixes decisions, work and history cannot be closed or cited.

The owner's general documentation standard already names `docs/developer/` and `docs/security/`
and says a repository moves to them when the root files are next restructured. This is that
restructuring.

## Decision

**D1 — A statement has exactly one home.**

| Kind | Home |
|---|---|
| A decision — what the operator does and why, what was rejected | an [ADR](README.md) |
| How the code works — a subsystem, an invariant, the contributor workflow | [docs/developer/](../developer/README.md); there is no `DEVELOPER.md` at the root |
| What somebody running the operator needs — installation, upgrades, what happens at runtime | [docs/operations/](../operations/README.md) |
| The threat model, and the gap each mechanism leaves | [docs/security/](../security/README.md) — one page per perspective, each closing with `## What this does not cover`. Reporting a vulnerability is [SECURITY.md](../../SECURITY.md) |
| Work still outstanding | a [ticket](../tickets/README.md), archived when the work lands |
| The plan, while it exists | [docs/planning/](../planning/) — consumed, see D10 |

**D2 — `README.md` is the front page and carries the reference.** The CRD fields, the Helm
values, the operator flags and the deterministic names are tables in the README and nowhere
else; a page under `docs/operations/` explains a setting without restating the table.

**D3 — A ticket is a work list and nothing else.** `docs/tickets/NNN-<slug>.md`, closed by
moving it to `docs/tickets/archive/` when the work lands. **The extraction is the close**: the
decision goes into an ADR, the operator-facing consequence into the README or
`docs/operations/`, the security-relevant one into `docs/security/`, the contributor knowledge
into `docs/developer/` — an archived ticket is history, never the source of a current rule.

**D4 — A number is never reused,** not an embargoed ticket's, not a merged ticket's. The
numbering command on the rules page reads deleted files from git history for that reason.

**D5 — A finding goes into an existing ticket first.** The open ticket of the same subject
collects it; a new ticket only when none fits. A collecting ticket is still one subject.

**D6 — A ticket carries no history.** Current state, required changes, open questions with an
answer line, nothing struck through, nothing dated. A changed fact is rewritten.

**D7 — An open security finding is embargoed.** A ticket with `security: live` or
`security: boundary` whose finding is not fixed keeps the `local_` prefix and stays untracked
through the repository's own `.gitignore` line; no tracked file, commit message or pull request
carries its details or its file name. The embargo ends at the fix, not at `state: done`; a
dropped or risk-accepted finding is published only on the owner's explicit, dated acceptance.

**D8 — Nothing outside `docs/tickets/` cites a ticket.** Not by number, label, file name or
path; cite the ADR instead. A ticket may cite an ADR; an ADR does not cite a ticket.

**D9 — A security page is one perspective, and it ends with its limits.** One page per
perspective under `docs/security/`, no single all-covering document, every page closing with
`## What this does not cover`; an open gap carries a stable `H-<n>` identifier in its heading.
A security page states what the tree does today — never "planned for".

**D10 — Planning documents are consumed, never maintained.** `docs/planning/` holds the plan,
the tombstone of the question catalog and the parked research the plan names.

- An answered question becomes an ADR, or an amendment in place of the record it changes, in
  the same session, and leaves the catalog. The catalog of this repository was consumed on
  2026-10-05 into [ADR 0012](0012-the-first-release-is-one-broker-run-from-git-and-high-availability-is-parked.md),
  [ADR 0013](0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md),
  [ADR 0014](0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md)
  and amendments of ADR 0001, 0002, 0006, 0007 and 0008; `docs/planning/questions.md` is a
  tombstone that says so. A new open decision lives in a ticket's `## Open questions` section.
- **A phase of the plan becomes tickets when it starts, in a session dedicated to that
  conversion** — a family ticket for the phase, children per deliverable. Until then the phase
  stays in [project-plan.md](../planning/project-plan.md), and a phase that ends is deleted
  from it.
- A measurement that a decision rests on is a fact about the code's environment and lives in
  [docs/developer/broker-behaviour.md](../developer/broker-behaviour.md), not in the plan.
- `docs/planning/` is deleted when the plan's last phase has started and the parked research
  has been either taken up by a phase or dropped. Nothing in code cites a planning document.

**D11 — English, everywhere.** Code, comments, commit messages, documentation. Conversation
with the owner may be German; the repository is not.

## Consequences

- Five directories and three root documents to keep in step: whoever changes behaviour
  updates the page that describes it in the same change, or the page is wrong.
- The extraction discipline makes closing a ticket slower than deleting it. That is the cost
  of archived tickets never being cited.
- The embargo puts security tickets outside git until fixed; a lost laptop loses them.
- Records that cited `DEVELOPER.md` or `SECURITY_ARCHITECTURE.md` by section number were
  rewritten to cite pages and headings in the same change.

## Alternatives Considered

- **Keep the three root files** (`README.md`, `DEVELOPER.md`, `SECURITY_ARCHITECTURE.md`).
  The owner's general standard already moved off them; the sibling project's reason — a
  document nobody finishes is a document nobody checks — applies to a 645-line security file
  as much as to theirs. Lost.
- **Keep the planning files under `docs/tickets/`.** They were not work lists, and the ticket
  rules (current state only, no history) cannot hold a catalog of 28 answered questions.
  Lost.
- **Convert the whole plan into tickets now.** Tickets for phases whose content will change
  before they start; the ticket rules do not fit that. Lost to D10's per-phase conversion.
- **Delete the question catalog outright.** Its answers are in the records now, but the
  records' `Status` sections say where they came from; the tombstone keeps that sentence
  resolvable until `docs/planning/` goes. Lost.

## Residual risks

- D8 has no automated check. `git grep -nE '\bT[0-9]+\b|docs/tickets/[0-9]' -- ':!docs/tickets'`
  is the manual one.
- Not verified: that the sibling project's formats stay stable. This record copies their rules
  as of 2026-10-05; a later change there does not change this repository.

## References

- [docs/tickets/README.md](../tickets/README.md) — the ticket rules, frontmatter and body skeleton
- [docs/security/README.md](../security/README.md) — the form of a security page
- [docs/developer/README.md](../developer/README.md), [docs/operations/README.md](../operations/README.md)
- [docs/planning/project-plan.md](../planning/project-plan.md) — what D10 consumes
- [`.gitignore`](../../.gitignore) — the embargo line
