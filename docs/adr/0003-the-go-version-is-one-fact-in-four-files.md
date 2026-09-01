# ADR 0003: The Go version is one fact in four files, and one Renovate PR moves all four

## Status

Accepted. Date: 2026-09-01.

**Verified by reading, in this tree:** [`go.mod`](../../go.mod) line 3 (`go 1.27.0`),
[`Containerfile`](../../Containerfile) line 2 (`FROM golang:1.27.0-alpine AS builder`),
[`.github/workflows/build.yml`](../../.github/workflows/build.yml) line 8 and
[`.github/workflows/release.yml`](../../.github/workflows/release.yml) line 32 (both
`GO_VERSION: '1.27.0'`), [`.github/release-template.hbs`](../../.github/release-template.hbs)
line 25 (`![Go Version](https://img.shields.io/badge/go-1.27-blue)`),
[`renovate.json`](../../renovate.json) (six `customManagers`, the two `packageRules` carrying
`"groupName": "Go version"`), [`hack/verify-ci-references.mjs`](../../hack/verify-ci-references.mjs),
[`Makefile`](../../Makefile) lines 137-140 (`verify-ci-references`) and lines 371-376 (the
`ENVTEST_VERSION` pin and its comment), and the `release-tooling` job in
[`.github/workflows/release.yml`](../../.github/workflows/release.yml) line 1192, whose step at
line 1213 runs `make verify-ci-references`.

**Verified by running, on 2026-09-01:** `node hack/verify-ci-references.mjs` exits 0 and prints
`OK: all 6 Renovate customManagers reference real files and lines`, with the per-file hit counts
`.github/workflows/build.yml (1), .github/workflows/release.yml (1), .github/workflows/renovate.yml (0)`
for the `GO_VERSION` manager.

**Implemented:** all four locations have a `customManager`; all four share the `Go version`
group; `make verify-ci-references` proves each manager still matches a real file and line and
runs in CI on every pull request.

**Open, and stated as such:** nothing asserts that the four captured values *agree with each
other* — see Residual risks. Renovate itself has never been observed opening a grouped Go
version pull request against this repository; the grouping is read out of the configuration, not
measured. No workflow run of this repository has been observed at all: the whole pipeline landed
in a single commit (`feat: initial pipeline and project structure`) and I could not check
GitHub Actions history from here.

## Context

The Go toolchain version `1.27.0` is not a dependency of this repository, it is a *fact about*
it, and the fact is written down in four independent places that no compiler, linker or test
relates to one another:

| Location | Form | Files today |
|---|---|---|
| Module language version | `go 1.27.0` | [`go.mod`](../../go.mod) |
| Build stage base image | `golang:1.27.0-alpine` | [`Containerfile`](../../Containerfile) |
| CI toolchain | `GO_VERSION: '1.27.0'`, consumed as `go-version: ${{ env.GO_VERSION }}` | [`build.yml`](../../.github/workflows/build.yml), [`release.yml`](../../.github/workflows/release.yml) |
| Release-note badge | `go-1.27-blue` | [`.github/release-template.hbs`](../../.github/release-template.hbs) |

Four locations, five files today, because `GO_VERSION` is declared once per workflow that needs
it. `.github/workflows/renovate.yml` declares none — verified by grepping `GO_VERSION` across
`.github/workflows/`, which returns hits only in `build.yml` and `release.yml`.

Drift between them is not loud. A `go.mod` that asks for a newer language version than the CI
toolchain provides fails a build with a message about the toolchain, far from the file that
caused it. A `Containerfile` left behind builds the shipped binary with a compiler nobody tested
against. A stale badge is worse than either, because it is a published claim about a release
that is simply false and nothing in the pipeline reads it.

Two concrete failures during this port are the argument for everything below. Both were found
while wiring Renovate, both before the first pipeline commit, so `git log` cannot show them —
[`renovate.json`](../../renovate.json) arrived complete in one commit (verified:
`git diff --stat HEAD~1 HEAD -- renovate.json` reports 359 insertions and no deletions). This
ADR is the only record.

**Failure 1 — a location with no manager at all.** There was no `customManager` whose
`managerFilePatterns` selected `.github/workflows/*.yml`. Every regex that existed was correct
and every one of them matched. A Go bump would therefore have opened one grouped pull request
that moved `go.mod`, `Containerfile` and the badge and left `GO_VERSION` behind on both
workflows — a green pull request, automerged by the `minor`/`patch` rule, that quietly makes CI
build with a different compiler than `go.mod` asks for. It was found only because the regexes
were **run against the real files** instead of read; reading them proves each one is a good
regex, and says nothing about the file that has no regex.

**Failure 2 — a manager that matched and was still inert.** The badge manager's `matchStrings`
entry, `"go-(?<currentValue>\\d+\\.\\d+)-blue"` as JSON escapes it, matches line 25 of the
template and captures `1.27`. Its `versioningTemplate` was `"semver"`. A two-part value is not valid semver, so Renovate's
`isValid` for that versioning rejects the captured value and skips the update with
`invalid-value`, while the manager reports a match. **A matching regex is not enough: the
versioning API has to accept the captured value too.** The template is now `"loose"`, with
`extractVersionTemplate: "^(?<version>\\d+\\.\\d+)"`, and the reason is written into the
manager's own `description` field so the next reader does not re-derive it. Taken together with
failure 1, a Go bump at that moment would have moved two of the four locations, not four.

Both failures share one shape: **the configuration looked right and did nothing.** Renovate
reports no error for a manager that matches nothing — the manager simply contributes no
dependency. That is the failure mode a comment cannot catch and a reader will not notice,
because the comment says the automation exists.

## Decision

**D1 — The Go version is one fact, and every place that states it is covered by a
`customManager` whose `datasourceTemplate` is `golang-version`.** Four managers do this today:
`customManagers[1]` (Containerfile), `[2]` (go.mod), `[3]` (the `GO_VERSION` env of every
GitHub workflow) and `[4]` (the release-template badge). `customManagers[0]` (Makefile Go tools,
`datasource=go` from the `# renovate:` comments) and `[5]` (the Mosquitto broker image,
`datasource=docker`) are deliberately not Go-version managers and are not in the group.

**D2 — All four are in one Renovate group, so they cannot move apart.** The `packageRule`
whose `matchDatasources` is `golang-version` sets `"groupName": "Go version"` and
`"groupSlug": "go-version"`. A second rule pulls `gomod` packages matching
`"/^golang\\.org\\/x\\//"` into the same group, on the stated reason that `govulncheck` should see a
consistent set. **The group is what makes the four locations one change**, and the automerge
rule below is why the grouping has to be right rather than approximately right: `minor` and
`patch` updates on `golang-version` carry `"automerge": true`, so a partially populated group
merges itself. Only `major` requires review.

**D3 — A new place that names the Go version arrives together with its `customManager`, in the
same change.** A file added without one is not "not yet automated", it is a fifth copy of a
fact that three pull requests from now will disagree with the other four.

**D4 — No `customManager` is believed because its regex reads correctly. `make
verify-ci-references` runs it.** [`hack/verify-ci-references.mjs`](../../hack/verify-ci-references.mjs)
walks the repository (skipping `.git`, `bin`, `coverage`, `node_modules`, `tmp`, `vendor`),
resolves each manager's `managerFilePatterns` against the real file list, and executes each
`matchString` against the contents of the selected files. It fails on: a
`managerFilePatterns` entry that is not a valid regex, a glob it cannot interpret, a manager
with no `managerFilePatterns` or no `matchStrings`, zero selected files, zero matches for a
`matchString` across all selected files, and a match that captures no `currentValue` group —
for the reason the script states: "Without a `currentValue` Renovate has no version to compare
or replace, so a matching-but-valueless manager is still an inert one."

**D5 — The check is a Make target, entered the same way locally and in CI, and it runs before
anything is installed.** `make verify-ci-references` needs node only and no `npm install`; the
`release-tooling` job runs it ahead of its own `npm ci --ignore-scripts` step, "because it needs
nothing from node_modules". A guard that is expensive to reach is a guard
people skip.

**D6 — A comment that advertises automation which does not run is deleted or made true.** This
is the generalisation of both failures, and it is the sharper half of the rule: an inert
`customManager` is not merely useless, it is *worse than nothing*, because the comment beside it
tells the next person the pin is maintained and stops them looking. The Makefile already applies
this in the positive direction — `ENVTEST_VERSION ?= release-0.19` carries no `# renovate:`
comment and says why: "setup-envtest is pinned to a controller-runtime BRANCH, not a tag, so no
Renovate datasource resolves it and this pin is bumped by hand ... Deliberately no `# renovate:`
comment: the manager below only matches v-prefixed values, and a comment that matches nothing is
the failure mode `hack/verify-ci-references.mjs` exists to catch."

**D7 — Where a versioning template has to be something other than the obvious one, the
`description` of the manager says why.** `customManagers[4]` carries the reason for `loose` in
its own description. The next person to "fix" it back to `semver` reads the consequence first.

## Consequences

* **Every new location that names the Go version costs a `customManager`,** and a change that
  adds one without it fails `make verify-ci-references` only if the *manager* is broken — not if
  the *location* is uncovered. A file nobody wrote a manager for is invisible to this check. D3
  is a human rule, and this is the part of it that is not enforced.
* **`make verify-ci-references` proves each manager matches something. It does not prove the
  four values agree.** The script only asserts a non-empty `currentValue` capture; it never
  compares captures across managers. A hand edit that bumps `go.mod` and forgets the
  `Containerfile` passes this check.
* **The check cannot prove what Renovate will do with a match.** It runs the patterns through
  Node's regex engine; Renovate uses RE2, which rejects lookaround and backreferences that Node
  accepts. The script states this limit itself and asks that patterns stay plain. Failure 2's
  class — a match the versioning API then refuses — is likewise outside its reach: nothing in
  the repository reads `versioningTemplate`.
* **The badge is a published claim with no consumer.** Nothing renders it except a release note,
  so a stale badge cannot fail a build; it can only be wrong in public. That is precisely why it
  is in the group and not left to a human.
* Renovate itself runs from [`.github/workflows/renovate.yml`](../../.github/workflows/renovate.yml)
  on a daily schedule, on pushes to `main`, and on manual dispatch — never on a pull request. A
  broken manager therefore cannot be caught by watching Renovate on the pull request that broke
  it. `make verify-ci-references` is the only pull-request-time signal.
* The walk in `hack/verify-ci-references.mjs` reads every file selected by every manager on every
  run. It is cheap today; it is repository-wide by construction, so a manager with a broad
  `managerFilePatterns` makes it read more.

## Alternatives Considered

### Read the regexes carefully instead of running them

This is the practice failure 1 slipped past. Reading proves each regex that exists is well
formed. It cannot show the location that has no regex at all, and it cannot show a regex that matches a file whose
value the versioning API then rejects. Rejected as the primary control; it remains a useful
second pair of eyes.

### One source of truth, with the other locations reading it

`actions/setup-go` accepts `go-version-file`, so both workflows could drop `GO_VERSION` and read
`go.mod` directly. Not taken here, and the honest reason is that it shrinks the problem without
dissolving it: a `FROM golang:...` line cannot read a file, and neither can a Handlebars badge,
so the fact would still live in three places and still need a group and a check. Whether
`go-version-file` behaves as expected on the self-hosted runners this pipeline uses is **not
verified** — it has never been tried in this repository.

### Renovate's built-in managers only

The `gomod` and `dockerfile` managers cover `go.mod` and the `Containerfile` with no custom
regex at all. They cover neither the workflow env nor the badge, which are exactly the two that
broke. Rejected as insufficient, not as wrong: both built-in managers are still enabled and do
the work for their two files.

### Drop the badge from the release template

It would remove one location and one manager. Rejected: the badge is the only place a reader of
a published release note learns which toolchain built it, and deleting a fact to avoid
maintaining it is the wrong direction when the maintenance is one grouped pull request.

### A test that asserts all four values are equal

Attractive and not implemented. The obstacle is that the four are not written in the same form:
`go.mod`, the `Containerfile` and `GO_VERSION` carry `1.27.0` while the badge carries `1.27`, so
the test needs a normalisation rule, and a normalisation rule that is wrong in the other
direction would fail every legitimate patch bump. Recorded as an open item under Residual risks
rather than as a rejected idea — the comparison this repository actually needs and does not have
is precisely this one.

### Run Renovate itself in CI to prove the managers

Rejected for the pull-request path: it needs a token (`BOT_PAT`) and network access to the
`golang-version` datasource, and giving a fork-authored pull request a run of it on a
self-hosted runner is a larger change than the problem asks for. The offline regex run is the
cheap substitute, and its gaps are named above.

## Residual risks

* **Nothing asserts the four values are equal (open).** `make verify-ci-references` proves the
  four managers are live; a manual edit to one file still drifts silently until a build fails
  somewhere unrelated. This is the single biggest gap in the arrangement and it is accepted for
  now.
* **A location with no manager is invisible to the check (open).** D3 is enforced by review only.
* **The grouping has never been observed producing a single pull request.** It is read out of
  `renovate.json`; no Renovate run against this repository has been inspected. Unverified.
* **The `loose` fix for the badge has not been observed updating the badge.** What was measured
  is the failing direction (`semver` skipping with `invalid-value`, recorded during the port and
  not re-measured for this ADR); that `loose` plus `extractVersionTemplate` produces the intended
  two-part rewrite is inferred from Renovate's documented behaviour, not seen. Unverified.
* **RE2 compilation of every pattern is not proven** — the script says so in its own header.
* `make verify-ci-references` is reachable from no aggregate target: `all` is `build`, and
  `test` is `fmt vet envtest`. A developer who runs neither the target nor CI never runs it.
* Nothing in this repository has ever been executed against a real Kubernetes cluster, and no
  run of these workflows on GitHub has been observed. Every claim above about CI describes what
  the workflow files say, not what a run did.

## References

* [`renovate.json`](../../renovate.json) — the six `customManagers`, the `Go version`
  `packageRules`, and the `description` on `customManagers[4]` recording why its versioning is
  `loose`
* [`hack/verify-ci-references.mjs`](../../hack/verify-ci-references.mjs) — the check, and its own
  statement of what it cannot cover
* [`Makefile`](../../Makefile) — `verify-ci-references` (lines 137-140); the `ENVTEST_VERSION`
  and `GOCOVMERGE_VERSION` pins that deliberately carry no `# renovate:` comment
* [`go.mod`](../../go.mod), [`Containerfile`](../../Containerfile),
  [`.github/workflows/build.yml`](../../.github/workflows/build.yml),
  [`.github/workflows/release.yml`](../../.github/workflows/release.yml),
  [`.github/release-template.hbs`](../../.github/release-template.hbs) — the four locations
* [`.github/workflows/renovate.yml`](../../.github/workflows/renovate.yml) — when Renovate runs,
  and the workflow the `GO_VERSION` manager legitimately selects with zero hits
* [`package.json`](../../package.json) — the `verify:ci-references` script
* [`test/testimages/images.go`](../../test/testimages/images.go),
  [`internal/builder/statefulset.go`](../../internal/builder/statefulset.go),
  [`test/imagetools/image_tools_test.go`](../../test/imagetools/image_tools_test.go) — the same
  pattern applied to a second fact: one broker image pin in two files, moved by one manager and
  held equal by `TestPinnedImageIsTheOperatorDefault` — see
  [ADR 0007](0007-one-broker-image-pin-and-why-not-the-openssl-tag.md) for that pin itself
* [ADR 0010](0010-a-check-is-not-a-check-until-it-has-failed-on-purpose.md) — why
  `verify-ci-references` and its siblings are only trusted after they have been seen failing
