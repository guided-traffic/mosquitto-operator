# How a security page in this directory is written

This directory is the security architecture of the Mosquitto operator: what the operator and the
brokers it runs are trusted with, what the operator is allowed to do, where credentials and broker
data live, and — with the same weight — what the isolation it provides does **not** cover. One
page covers one perspective. There is deliberately no single page that covers all of them,
because a document nobody finishes is a document nobody checks.

This page describes the **form**. It names no individual page on purpose: the set changes, and an
index here would be a second place to keep current. The file names are the index.

## What belongs here, and what does not

| Statement | Home |
|---|---|
| What the operator and its brokers defend against, how, and what they leave open | here |
| Why a decision was taken, what was rejected | an [ADR](../adr/README.md) |
| How the code works, for somebody changing it | [docs/developer/](../developer/) |
| What somebody installing or running the operator configures — the CRD, the chart values, the flags | [README.md](../../README.md) |
| Work still outstanding | a ticket, never a page here |
| How to report a vulnerability | [SECURITY.md](../../SECURITY.md) at the root |

A page here is written for somebody who has to judge whether the operator is safe enough for their
cluster — a cluster administrator installing it, a namespace owner deciding who may create a
`Mosquitto`, an auditor, a contributor changing something security-relevant. It names files and
functions where that makes a claim checkable, the way a developer page does, and it therefore goes
stale when the tree moves: **whoever changes the behaviour updates the page in the same change.**

## The shape of a page

1. **A title that names the perspective**, not the mechanism. The reader is choosing a page from a
   directory listing.
2. **One paragraph under it** saying what the page covers, and pointing at the neighbouring
   perspective a reader may actually have wanted.
3. **The body**, in `##` sections. No numbered sections — see *Citing* below.
4. **A closing `## What this does not cover`.** Every page ends with one. A page that cannot name
   its own limits has not been thought through, and a reader who finds no limits section assumes
   there are none.

Open gaps live in that closing section of the page whose mechanism has the gap, never in a list of
their own.

## Ground rules

- **Every claim is verified against the code before it is written.** Read the type, the function,
  the test, the manifest; render the chart and the kustomize overlay rather than reading YAML by
  eye. Where something could not be verified, the page says so in the sentence that makes the
  claim — "not verified, and this is the gap" is a complete statement. An assumption must never
  travel as a fact.
- **Say what backs a claim.** Nothing in this repository has been observed running on a real
  cluster. A claim is read from the code, rendered from the manifests, asserted by a test tier, or
  measured against the pinned broker image — and a claim that depends on what only a cluster shows
  (kubelet mount semantics, admission, garbage collection, what an endpoint really emits) says that
  it was not observed. envtest runs no kubelet and no garbage collector, so a passing integration
  test proves what the API server accepts, never that a pod started.
- **Where the code and this directory disagree, the code wins**, and the disagreement is named
  rather than quietly corrected.
- **A gap is documented, not hidden.** Name which mechanism, which adversary, whether it is live
  today or dormant, and what an operator can do in the meantime.
- **No tickets.** No checkbox lists, no owners, no dates for work that has not happened, no
  "planned for". A page here states what the tree does today. Outstanding work is a ticket and
  lives elsewhere; a decision is an ADR and lives elsewhere.
- **History only where it carries a rule**, as prose inside the mechanism's own section.
- **A fixed identifier for an open gap.** An open gap carries an `H-<n>` in its heading, and the
  number never changes or gets reused, because reports and conversations name it. An explicit
  `<a id="h-<n>"></a>` anchor sits above the heading. The next number is one above the highest in
  use — `grep -rn '^### H-' docs/security/` lists them.
- **Security notes only where a real security relation exists.**
- **English, always.**

## Citing

Cite a page by its file name and its heading, never by a section number. Cite an ADR by its number
and the decision (`ADR 0009 D5`).
