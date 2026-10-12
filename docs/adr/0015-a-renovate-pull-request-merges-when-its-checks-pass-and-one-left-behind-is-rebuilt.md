# ADR 0015: A Renovate pull request merges when its checks pass, and one left behind is rebuilt

## Status

Accepted. Date: 2026-10-12. The call is the maintainer's, made on 2026-10-12 after three options
were put to them: keep `rebaseWhen: "conflicted"` and document the manual recovery, switch to
`"auto"` alone, or `"auto"` together with required checks on `main` (this record).

**Implemented in this change:** `"rebaseWhen": "auto"` in [`renovate.json`](../../renovate.json),
the comment in [`renovate.yml`](../../.github/workflows/renovate.yml) that rested on the old value,
and the pages that describe both. **Implemented outside the tree, right after this change
merged:** the repository ruleset of D2 and D3. A ruleset is a repository setting; `git log` does
not show it, `gh api repos/guided-traffic/mosquitto-operator/rulesets` does.

**Verified by reading** Renovate 44.149.1, the version the workflow ran on 2026-10-10:
`determineRebaseWhenValue` in `lib/workers/repository/update/branch/reuse.ts` turns `"auto"` into
`behind-base-branch` when the branch has `automerge === true` (and no merge queue and no
keep-updated label apply), otherwise into `conflicted` unless the platform forces up-to-date
branches; a `behind-base-branch` branch that is behind and unmodified is rebuilt, a modified one is
left alone. `writeUpdates` in `lib/workers/repository/process/write.ts` returns as soon as a branch
was automerged, and `renovateRepository` in `lib/workers/repository/index.ts` restarts the job once
after that ("Automerged but already retried once").

**Verified on GitHub:** pull request #55 went green at 14:51 UTC on 2026-10-10 and was merged at
15:21 by `guided-traffic-automation[bot]` inside a dispatched Renovate run; its timeline holds no
`auto_merge_enabled` event and its `autoMergeRequest` is empty. `platformAutomerge: true` had
never taken effect: `main` carried no rule that names a check, and GitHub enables auto-merge only
on a pull request with a requirement still open.

**Not yet observed:** GitHub auto-merge engaging for a pull request the app opens; the release
commit of `semantic-release` passing the ruleset through the app's bypass. Both happen first with
the next Renovate update and the next `fix` or `feat` on `main`.

## Context

On 2026-10-09 new advisories against Go 1.27.1 and `golang.org/x/net` v0.59.0 turned `main` red,
and with it every open Renovate pull request: each failed `Vulnerability Check` and
`Container Malware Scan` because of the `main` it was built on, not because of its own update.
The Go update that fixed it merged on 2026-10-10. The others stayed red. Under
`rebaseWhen: "conflicted"` Renovate rebuilds a branch only when it stops applying; a branch that is
red because of its base still applies, so it is never rebuilt, never turns green and never merges.
The sbom-action update had been red since 2026-10-07 on a flaky E2E assertion for the same reason.
What recovered them was a human ticking "rebase all open PRs" in the Dependency Dashboard. The
comment in `renovate.yml` had justified the setting with "the rest were already correct"; that is
the assumption that failed.

A second limit existed without any failure. With no GitHub auto-merge, Renovate merged pull
requests itself, during its run, and a run ends after one automerge and restarts once: at most two
merges a night. `main` shows exactly two on 2026-10-08 (#48, #49) and on 2026-10-09 (#50, #51).

`"auto"` alone would cure the first and starve the second. It rebuilds an automerge branch that is
behind `main`, and every push to `main` puts every branch behind: after a day with any merge, the
nightly run rebuilds every branch and merges none, and after a quiet day it merges one, because the
first merge puts the rest behind again.

## Decision

**D1 — GitHub merges a Renovate pull request the moment its required checks pass.**
`platformAutomerge: true`, `automergeType: "pr"` and `automergeStrategy: "squash"` stay as they
were; what makes them work is D2, because GitHub enables auto-merge only on a pull request with a
requirement still open. A merge no longer waits for a Renovate run, and a run no longer limits how
many merge.

**D2 — `main` carries one ruleset with one rule: required status checks, not strict.** Twelve
contexts, each bound to the GitHub Actions app (integration ID `15368`) so no other sender can
satisfy them: `Code Linting`, `GoSec Security Scan`, `Malware Scan (Source Code)`,
`Container Malware Scan`, `Unit Tests`, `Integration Tests (envtest)`, `Mosquitto Image Tools`,
`Vulnerability Check`, `Cyclomatic Complexity`, `Generated Manifests Up To Date`,
`Release Tooling` and `E2E Tests`. Left out on purpose: the two matrix legs, because the gate
carries their result under a stable name
([ADR 0004](0004-two-e2e-legs-and-no-version-matrix.md) D7); `Combined Coverage Report`, a report
and not a check; `Semantic Release`, which runs on no pull request. **A skipped required check
does not block a merge**, so a required job that has `needs:` is only required together with
every job it needs: `Container Malware Scan` needs `Malware Scan (Source Code)`, which is
required, and `E2E Tests` is the gate that runs `if: always()`. Not strict: a pull request that is
behind `main` may merge, so one merge does not hold back the next.

**D3 — One bypass: the app `guided-traffic-automation` (ID `5070048`), always.**
`semantic-release` pushes `chore(release): <version> [skip ci]` to `main` itself, a commit no check
runs on, so without the bypass the ruleset refuses every release. Repository admins have no
bypass: a direct push to `main` is refused for them as for anyone. An admin who needs out edits
or disables the ruleset — an audited action, not a silent one.

**D4 — `"rebaseWhen": "auto"`.** A branch that automerges is rebuilt when it falls behind `main`;
one that waits for a human — majors, minors of the broker image, npm, and any branch a human has
pushed to — only when it conflicts. With D1 a green automerge branch has merged before it can fall
behind, so what the nightly run rebuilds is red or pending, now on the current `main`.

**D5 — The required list is part of the pipeline.** A job added to, renamed in or removed from
[`release.yml`](../../.github/workflows/release.yml) is checked against D2 before it merges:
a renamed or removed required job leaves a context that nothing reports, and every pull request
waits on it until the ruleset is edited; a new job is not required until it is added. The list is
written down in [ci-and-release.md](../developer/ci-and-release.md#what-reaches-main) and in
[adding-things.md](../developer/adding-things.md#a-ci-job).

## Consequences

* **Renovate pull requests merge within minutes of turning green**, day or night. Nobody reads
  them more than before; the automerged action, module and image updates of
  [H-9](../security/ci-and-supply-chain.md#h-9) arrive sooner.
* **Each nightly run rebuilds every automerge branch that is behind and not green**: one
  `Test and Release` per such branch per night on the shared runners.
* **A flaky test is retried nightly, and a flake that passes on retry merges.** A pull request
  stuck red on a flake used to be the visible trace of the flake; it no longer is.
* **Every pull request to `main` needs the twelve checks**, the maintainer's too. A change that
  touches `.github/workflows/` can be merged only by a token with the `workflow` scope, which the
  web interface has.
* **The setting lives outside the tree.** Its current state is what the API returns, not what this
  record says; the record says what it was set to and why.
* **Merged combinations are still untested** (not strict, as before): two pull requests green on
  their own may break together; `main`'s own run is where that shows.

## Alternatives Considered

### Keep `"conflicted"`, and document the recovery

No extra runner time and up to two merges a night. Every red `main` — every Go release that
carries an advisory — leaves the open pull requests red until a human ticks "rebase all".
Rejected: that is the hand step this change exists to remove.

### `"auto"` without required checks

Self-healing, and starving: after any push to `main` during the day, the nightly run rebuilds
every branch and merges none (Context). Rejected after the cost was computed from the source; it
had first been offered with the cost understated.

### Strict required checks

Every merge puts the remaining pull requests behind, and GitHub merges none of them until each is
rebuilt — the nightly one-at-a-time of the previous alternative, by another route. Rejected.

### A merge queue

The queue tests each pull request against the head of `main`, which closes the untested
combination; Renovate turns `"auto"` into `"conflicted"` when it sees one. It needs a
`merge_group` trigger in `release.yml` and a full run per queue entry on the self-hosted runners.
Not chosen now; it is the next step if broken combinations start to show on `main`.

### Run Renovate on every push to `main`

Rejected earlier in [`renovate.yml`](../../.github/workflows/renovate.yml): every merge would
start a wave of pull requests on runners that serialise.

### A bypass for admins

Would keep direct pushes to `main` open for the maintainer. Not taken: the maintainer chose
"direct pushes refused" and editing the ruleset is the way out that leaves a trace.

## Residual risks

* **The app pushes to `main` past every check** (D3), and whoever holds `APP_PRIVATE_KEY` holds
  that. Before this change `main` had no rule at all, so nothing is lost; it is now written down
  ([H-21](../security/ci-and-supply-chain.md#h-21)).
* **A new job is not required until someone adds it** (D5) — the list fails open for additions and
  closed for renames.
* **Not observed yet:** auto-merge for an app-opened pull request, and the release push through
  the bypass (Status).
* **Untested combinations and retried flakes** (Consequences).

## References

* [`renovate.json`](../../renovate.json) — `rebaseWhen`, `platformAutomerge`, `automergeType`,
  `automergeStrategy`
* [`.github/workflows/renovate.yml`](../../.github/workflows/renovate.yml) — when Renovate runs, and
  why not on push
* [`.github/workflows/release.yml`](../../.github/workflows/release.yml) — the jobs behind the twelve
  contexts, `e2e-gate`, and the `semantic-release` push
* [ci-and-release.md](../developer/ci-and-release.md#what-reaches-main) — the required list, as the
  contributor-facing page
* [ci-and-supply-chain.md](../security/ci-and-supply-chain.md#h-21) — what the bypass leaves open
* [ADR 0004](0004-two-e2e-legs-and-no-version-matrix.md) D7 — why the gate and not a leg
* [ADR 0005](0005-fork-pull-requests-execute-on-the-self-hosted-runners.md) — the app token and who
  holds it
