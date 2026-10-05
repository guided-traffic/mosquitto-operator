---
id: T1
title: the e2e jobs are commented out and three documents still say they run
state: analysed
severity: medium
security: none
threat:
urgency: now          # rule 1: measured-false statements in tracked files
effort: S
blocked-by:
filed-from: the documentation restructuring of 2026-10-05
opened: 2026-10-05
decided:
done:
---

## Current state

The E2E tier's two legs and the `e2e-gate` job are commented out in
[`release.yml`](../../.github/workflows/release.yml#L37-L57) since 2026-09-01; the comment there
names the trigger (an ARC runner pod losing its network during `apt-get install`, not a defect in
the tree) and the restore procedure. `semantic-release` lists twelve jobs in its `needs:`
([`release.yml:1253`](../../.github/workflows/release.yml#L1253)), without `e2e-tests`. The same
comment says `main` carries no branch protection.

Two tracked documents still describe the tier as running:

- [ADR 0004](../adr/0004-two-e2e-legs-and-no-version-matrix.md) `Status` says "Implemented: the
  two legs, the gate job, …" and nowhere that they are disabled; its decisions about the stable
  `E2E Tests` status context assume a required check that does not exist.
- [`.github/release-template.hbs:28-31`](../../.github/release-template.hbs#L28-L31) tells every
  release reader that a release is cut only after "unit, integration and E2E tests" went green.

[ADR 0005](../adr/0005-fork-pull-requests-execute-on-the-self-hosted-runners.md) D4 still lists
`e2e-tests` among the jobs that hold a credential, and its Context counts 18 jobs; 16 run today.

Impact: every release since 2026-09-01 shipped without an E2E run while its notes said otherwise,
and the plan's E2E verifications run only by hand until the jobs return. The project plan's
phase 1 restores the jobs; this ticket is the statements, which are wrong today.

## Required changes

1. ADR 0004 `Status`: record that the jobs are commented out since 2026-09-01, why, and that
   restoring them is the first deliverable of phase 1 of the plan; the index row in
   [docs/adr/README.md](../adr/README.md) already says "Partly built" and stays in step.
2. `release-template.hbs`: drop "and E2E tests" from the gating sentence until the jobs return,
   then restore it in the change that restores them. Verify with `make test-release-tooling`,
   which renders the template.
3. ADR 0005: D4's job list and the Context's count corrected to the running jobs.
4. When the jobs are restored (plan phase 1): `e2e-tests` back in the `needs:` of
   `semantic-release`, ADR 0004's `Status` back to "Implemented", the template sentence back.

## Not verified

- Whether any E2E leg ever completed in CI before 2026-09-01; the comment says the single-node leg
  "passed" the same step, not that the suite passed.
- Whether branch protection is set on `main`; the comment says not, and that is not checkable from
  the tree.
