# ADR 0005: Fork pull requests execute on the self-hosted runners, gated outside the repository

## Status

Accepted. Date: 2026-09-01. The call is the maintainer's, made on 2026-09-01 after the exposure
below was put to them explicitly.

**Verified by reading**
[`.github/workflows/release.yml`](../../.github/workflows/release.yml) — the comment block above
`on:`, the `on:` block itself, the `concurrency` block, the top-level `permissions:`, every
`runs-on:` in the file, every `permissions:` block, every `secrets.` reference, the
`persist-credentials: false` checkouts, the `npm ci --ignore-scripts` step and the `if:` on
`semantic-release`; [`.github/workflows/build.yml`](../../.github/workflows/build.yml) in full;
[`.github/workflows/renovate.yml`](../../.github/workflows/renovate.yml) in full. The counts
below come from `grep -c 'runs-on: self-hosted'` per file and a `grep -n 'runs-on:'` filtered for
anything that is not `self-hosted`, which returns nothing.

**Not verified, and it matters.** Two of the load-bearing facts cannot be checked from this
tree at all:

* **The repository setting that gates fork runs.**
  `Settings > Actions > General > "Fork pull request workflows from outside collaborators"` set
  to `"Require approval for all outside collaborators"` is asserted by the comment at the top of
  [`.github/workflows/release.yml`](../../.github/workflows/release.yml) and by the maintainer.
  It is not a file, nothing in CI checks it, and this ADR could not confirm it. **If that setting
  is not what the comment says it is, the entire control described here does not exist.**
* **The runner fleet.** The matrix comment states that each leg "lands on its own ephemeral ARC
  runner pod". Whether the runners are in fact ephemeral, what else shares their node, and what
  credentials live on them are properties of infrastructure outside this repository. Not checked.

Nothing in this repository has ever run: there is no workflow run, no fork pull request and no
observed runner behaviour behind any statement here. Everything about GitHub's own behaviour
(what a fork run receives, which workflow definition a `pull_request` event uses, how a skipped
required check is counted) is platform behaviour reasoned from documentation, marked as such
where it appears, and not reproduced here.

Implemented: every `runs-on:`, every `permissions:` block, the `persist-credentials: false`
checkouts and the `--ignore-scripts` install all exist in the tree today. Open: the residual
items below, none of which has code attached.

## Context

The repository is public, at `guided-traffic/mosquitto-operator`. All 18 jobs across the three
workflows run on `self-hosted` runners — 15 in
[`.github/workflows/release.yml`](../../.github/workflows/release.yml), 2 in
[`.github/workflows/build.yml`](../../.github/workflows/build.yml), 1 in
[`.github/workflows/renovate.yml`](../../.github/workflows/renovate.yml) — with no exceptions.
[`.github/workflows/release.yml`](../../.github/workflows/release.yml) triggers on
`push` to `main`, on `pull_request` to `main`, and on `workflow_dispatch`.

Those two sentences together mean that a pull request opened from a fork of a public repository
runs code the fork author wrote, on infrastructure the maintainer owns. That is not incidental to
the pipeline; it is what the pipeline does. The `e2e-tests` job in particular runs
`sudo apt-get install`, `sudo modprobe`, `sudo sysctl -w`, `docker save`, `docker exec` into Kind
nodes and `make docker-build` — the workflow's own steps assume a runner account with
unprompted `sudo` and access to a Docker daemon, and both of those are root-equivalent on the
host.

An earlier review of this pipeline got the security shape of that wrong, and the correction is
the reason this ADR exists. **The two halves have to be stated separately:**

* **Secrets are not the exposure.** GitHub does not pass repository secrets to a workflow run
  triggered by a `pull_request` event from a forked repository. The run receives a `GITHUB_TOKEN`
  with read-only permissions and nothing else. In a fork run, `secrets.DOCKERHUB_PAT` and
  `secrets.BOT_PAT` are empty strings.
* **Code execution is the exposure.** A fork PR causes fork-authored source, a fork-authored
  `Containerfile` and a fork-authored `Makefile` to be built and executed on the runner fleet.
  Under a `pull_request` trigger — as opposed to `pull_request_target` — GitHub also uses the
  workflow definition from the pull request's merge commit, so the *steps themselves* are
  fork-authored too. There is no subset of the repository's contents that a fork PR cannot
  change and have executed.

The alternative controls all cost something the maintainer decided not to pay. What follows is
what was decided instead.

## Decision

**D1 — Every job runs on `self-hosted`, in all three workflows, with no exceptions.** There is
no mixed fleet: `grep -n 'runs-on:'` across
[`.github/workflows/`](../../.github/workflows/) returns `self-hosted` on every line. A job that
moves to a hosted runner is a change to this ADR, not a local optimisation, because the split it
creates is the thing D5 is about.

**D2 — The `pull_request` trigger stays, and fork-authored code executing on the fleet is
accepted.** The pipeline is the only evidence a reviewer has about a contribution: linting,
`gosec`, `govulncheck`, the coverage delta, the malware scans and both E2E legs
([ADR 0004](0004-two-e2e-legs-and-no-version-matrix.md)). A contribution that arrives with none
of that has to be reviewed by reading alone, which for an operator that provisions StatefulSets
is not a review.

**D3 — No fork guard lives in any workflow file.** No job carries an
`if: github.event.pull_request.head.repo.full_name == github.repository` or an equivalent. The
control is `Settings > Actions > General > "Fork pull request workflows from outside
collaborators"`, set to `"Require approval for all outside collaborators"` — a per-run human
approval, held outside the repository. The comment above `on:` in
[`.github/workflows/release.yml`](../../.github/workflows/release.yml) records that this is
where the control lives, so a reader meets the decision at the top of the file that carries the
risk rather than deducing it from fifteen `runs-on:` lines. **The setting is the whole control.
It is not verifiable from this tree, and this ADR does not claim it is** — see Residual risks.

**D4 — The accepted exposure is code execution on the runner, and it is never described as
credential disclosure.** `secrets.DOCKERHUB_PAT` is referenced by three jobs in
[`.github/workflows/release.yml`](../../.github/workflows/release.yml) (`e2e-tests`,
`mosquitto-image-tools`, `container-malware-scan`, each in a `docker/login-action@v4` step) and
by two steps in [`.github/workflows/build.yml`](../../.github/workflows/build.yml) (the Docker
Hub login and the `docker/scout-action@v1` scan). `secrets.BOT_PAT` is referenced by
`semantic-release` in [`.github/workflows/release.yml`](../../.github/workflows/release.yml)
(the checkout `token:` and the `GITHUB_TOKEN` env of the Release step) and by
[`.github/workflows/renovate.yml`](../../.github/workflows/renovate.yml). **None of those values
reaches a fork run.** Any future statement of this risk states it the same way: the runner
executes untrusted code; it does not hand out a token.

**D5 — Only [`.github/workflows/release.yml`](../../.github/workflows/release.yml) is reachable
from a fork pull request, and the job that holds `BOT_PAT` is unreachable twice over.**
[`.github/workflows/build.yml`](../../.github/workflows/build.yml) triggers only on
`release: types: [published]`;
[`.github/workflows/renovate.yml`](../../.github/workflows/renovate.yml) only on `schedule`,
`push` to `main` and `workflow_dispatch`. Neither has a `pull_request` trigger, so neither runs
for a fork PR at all. Inside `release.yml`, `semantic-release` carries
`if: github.event_name == 'push' && github.ref == 'refs/heads/main'`, so it does not run on any
pull request — fork or not — independently of the secret rule. **New workflows keep this shape:
a workflow that needs a repository secret does not also carry a `pull_request` trigger.**

**D6 — The default job token is read-only, and a job that needs more raises it in its own
block with a comment saying why.**
[`.github/workflows/release.yml`](../../.github/workflows/release.yml) sets top-level
`permissions: contents: read`, so all 15 jobs start there. Exactly two raise it:

| Job | `permissions:` | Why, per the file |
|---|---|---|
| `coverage-report` | `contents: read`, `pull-requests: write` | "Needed to comment on PRs" — the `marocchino/sticky-pull-request-comment@v3` step; `contents` is re-stated as `read` with the note that `semantic-release` commits the badge |
| `semantic-release` | `contents: write`, `issues: write`, `pull-requests: write`, `id-token: write` | tag, release, and the coverage badge commit |

[`.github/workflows/renovate.yml`](../../.github/workflows/renovate.yml) sets top-level
`permissions: contents: read` and its single job raises nothing — it authenticates through
`secrets.BOT_PAT`, not through the job token, so the job token has nothing to do.

**D7 — In [`.github/workflows/build.yml`](../../.github/workflows/build.yml) the permission is
declared per job, and every job added there declares its own.** That file carries **no top-level
`permissions:` block**; `build` and `release-helm-gh` each declare `permissions: contents:
write`, with a comment naming the reason — the SBOM upload to the release, and the commit to the
`gh-pages` branch respectively. Both comments also state what is deliberately *not* granted:
`build` touches no Pages, attestations or OIDC token, and `release-helm-gh` needs no
`pages: write` because it publishes by committing, never by calling the Pages deployment API.
The missing top-level floor is a real gap, recorded in Residual risks rather than papered over.

**D8 — A job token is never left in a working tree that becomes a build context, and the job
that holds `BOT_PAT` does not execute dependency lifecycle scripts.**
`persist-credentials: false` is set on three checkouts: `container-malware-scan` and
`semantic-release` in [`.github/workflows/release.yml`](../../.github/workflows/release.yml), and
`build` in [`.github/workflows/build.yml`](../../.github/workflows/build.yml). The first and
third are the jobs whose `Containerfile` does `COPY . .`, where a persisted token in
`.git/config` would travel into the image — `.dockerignore` excludes `.git/` as well, and the
comments say plainly that both halves are kept because either one alone is a single point of
failure. On `semantic-release` the reason is different and is stated in the file: the checkout
would otherwise write `BOT_PAT` into `.git/config` as an `http.extraheader` readable by every
later step, and `npm ci --ignore-scripts` in that same job exists so the install of the release
toolchain cannot run arbitrary lifecycle scripts while `BOT_PAT` sits in the environment.

## Consequences

* **A fork pull request runs arbitrary code as the runner user, on a fleet that has a Docker
  daemon and unprompted `sudo`.** Approval by a maintainer is the only thing between an opened
  PR and that execution. Approving a fork PR is therefore not a courtesy click; it is the
  security decision of this pipeline, and it has to be made after reading the diff — including
  the diff of `.github/workflows/`, the `Containerfile` and the `Makefile`.
* **Approval is per run, not per contributor.** A pull request that was approved and then pushed
  to needs approving again (GitHub platform behaviour, not verified here). A reviewer who
  approves once and stops reading later pushes has silently removed the control.
* **The gate covers outside collaborators only.** Anyone with write access to the repository
  opens a branch PR, not a fork PR, and their code runs on the fleet with no approval step at
  all. That is the intended meaning of write access, and it is also the reason the runner fleet
  is not a boundary that protects anything from a compromised maintainer account.
* **Fork pull requests are expected to fail, and not on their merits.** Three jobs call
  `docker/login-action@v4` with `password: ${{ secrets.DOCKERHUB_PAT }}`, which is an empty
  string in a fork run; and `coverage-report` cannot be granted `pull-requests: write` on a fork
  PR, because the token is read-only there, so its sticky-comment step — which carries no
  `continue-on-error` — has no permission to post. **Neither outcome was observed**; both are
  derived from the platform rule in the Context and from reading the steps. The practical shape
  is that a fork contributor sees red checks caused by the pipeline rather than by their change,
  and that has to be explained to them by hand.
* **A hosted-runner escape hatch does not exist.** With D1 there is no job a contributor can
  point at and say "at least that one ran". The pipeline is all-or-nothing on the fleet.
* **The in-tree record is a comment, and comments do not enforce.** The block above `on:` in
  [`.github/workflows/release.yml`](../../.github/workflows/release.yml) tells a reader the risk
  is accepted and where the gate is. It cannot detect the gate being switched off.

## Alternatives Considered

### A per-job fork guard in the workflow

Costed and rejected. It would have to be repeated on 15 jobs in
[`.github/workflows/release.yml`](../../.github/workflows/release.yml), where a single job
missing the guard is invisible and defeats it entirely. It also breaks the gate job of
[ADR 0004](0004-two-e2e-legs-and-no-version-matrix.md): with `e2e-tests` skipped,
`needs.e2e-tests.result` is `skipped` rather than `success`, and `e2e-gate` asserts
`[ "${result}" = "success" ]`, so the required check would fail on every fork PR until the gate
was taught a second meaning of "acceptable". Most importantly it does not improve the review
position it is supposed to protect: a guarded fork PR arrives with no test evidence at all, and
the maintainer ends up running the branch locally — on a machine with far more on it than a
runner.

### Splitting the cheap jobs onto GitHub-hosted runners

Costed and rejected. The cheap jobs are exactly the ones whose execution matters least. The
expensive job cannot move: `e2e-tests` prepares the *host* — `modprobe` for the kube-proxy
kernel modules, `sysctl` for the bridge and forwarding settings, per-node
`ctr --namespace=k8s.io images import` into the Kind nodes — and that preparation assumes a host
the workflow controls. So the split would leave fork-authored code executing on the fleet in the
one job that builds and runs a container image, while doubling the maintenance of every shared
step (`Set up Go`, `Install build tools`, the `sudo apt-get` lines) across two runner
environments that then drift. The exposure would be narrowed, not removed, and paid for in
divergence.

### `pull_request_target` instead of `pull_request`

Rejected as strictly worse. It runs the *base* branch's workflow with access to secrets, against
a fork's code — trading "untrusted code, no secrets" for "untrusted code, with secrets". Every
mitigation would then have to be written by hand inside the workflow, and getting one of them
wrong leaks `DOCKERHUB_PAT` and `BOT_PAT` rather than costing runner time.

### Making the repository private

Rejected: it is a public operator, and outside contributions are the point of publishing it.

### Dropping the `pull_request` trigger

Rejected — see D2. It removes the exposure by removing CI from pull requests, which is the same
as removing CI.

### Requiring approval for *all* outside contributors including returning ones

Not a separate option in the setting; `"Require approval for all outside collaborators"` is
already the strictest of the fork-run choices GitHub offers short of disabling fork runs
entirely, and that last one is the previous alternative under another name.

## Residual risks

* **The control is a repository setting this repository cannot see (open).** Nothing in the tree
  reads, asserts or alerts on
  `Settings > Actions > General > "Fork pull request workflows from outside collaborators"`. If
  it is changed — by anyone with admin rights, at any time, for any reason — every fork PR runs
  immediately and no file in this repository changes. This ADR could not verify the current
  value.
* **Runner isolation is asserted, not verified (open).** The matrix comment describes "its own
  ephemeral ARC runner pod". If the runners are not ephemeral, fork-authored code from one PR
  can leave state — a poisoned build cache, a modified `~/.docker/config.json`, a cron entry —
  that a later run of a *trusted* job executes with real secrets in its environment. That is the
  path from "code execution, no secrets" to "secrets", and it runs entirely through
  infrastructure this repository cannot inspect.
* **Root-equivalence on the runner is inferred from the steps (open).** The workflow calls
  `sudo` without a password prompt and drives a Docker daemon. If the runner account really has
  passwordless `sudo`, fork-authored code runs as root on the host. The steps are written
  assuming it works; nothing here confirms it does.
* **[`.github/workflows/build.yml`](../../.github/workflows/build.yml) has no top-level
  `permissions:` floor (open).** Both of its current jobs declare their own, so the file is
  correct as it stands — but a job added without a `permissions:` block inherits the
  repository-wide default, which may be read/write on all scopes.
  [`.github/workflows/release.yml`](../../.github/workflows/release.yml) and
  [`.github/workflows/renovate.yml`](../../.github/workflows/renovate.yml) do not have this
  problem, because their top-level `contents: read` applies to any job that forgets. Adding the
  same floor to `build.yml` is a one-line hardening item that has not been done.
* **`coverage-report` writes to pull requests with the default token (accepted).** It is the one
  job in `release.yml` that holds `pull-requests: write` while running on a PR, and its message
  interpolates step outputs and `env.COVERAGE_SUMMARY`, which is built from
  `coverage/summary.txt` — a file produced from the checked-out tree, and therefore
  fork-influenced content on a fork PR. On a fork PR the token cannot actually post, so the path
  is not reachable there today; on a branch PR the content originates from someone who already
  has write access. The risk is real only if the token behaviour changes, and it is recorded so
  that a future change to that step is made with it in view.
* **No workflow run exists (open).** Every consequence above describes what the files say will
  happen. The first fork pull request is what turns them into observations, and the first one
  should be treated as an experiment: watch the Docker Hub login steps and the sticky comment,
  and expect to explain red checks that are not the contributor's fault.

## References

* [`.github/workflows/release.yml`](../../.github/workflows/release.yml) — the accepted-risk
  comment above `on:`, the `pull_request` trigger, the top-level `permissions: contents: read`,
  the `coverage-report` and `semantic-release` permission blocks, the `DOCKERHUB_PAT` logins,
  the `BOT_PAT` checkout and Release step, `persist-credentials: false`, `npm ci
  --ignore-scripts`
* [`.github/workflows/build.yml`](../../.github/workflows/build.yml) — the
  `release: types: [published]` trigger, the two per-job `contents: write` blocks and their
  comments, `persist-credentials: false` on the build checkout
* [`.github/workflows/renovate.yml`](../../.github/workflows/renovate.yml) — the top-level
  `contents: read`, the `schedule`/`push`/`workflow_dispatch` triggers, the `BOT_PAT` token
* [`Containerfile`](../../Containerfile) and [`.dockerignore`](../../.dockerignore) — the
  `COPY . .` build context D8 keeps the job token out of
* [ADR 0004](0004-two-e2e-legs-and-no-version-matrix.md) — the two E2E legs that run on this
  fleet, and the `e2e-gate` job a fork guard would have broken
