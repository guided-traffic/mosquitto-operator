# Continuous integration and the release

The three workflows, what each job runs, how a release is cut and published, and Renovate. Every
check is entered through a Make target ([build-test-lint.md](build-test-lint.md)), so a local run
and a CI run are the same invocation. Read against the tree on 2026-10-05.

**Every job runs on `runs-on: self-hosted`**, in all three workflows. This repository is public,
so a fork pull request executes fork-authored code on that infrastructure. Repository secrets are
*not* passed to a fork run — GitHub provides only a read-only `GITHUB_TOKEN` — so the exposure is
code execution on the runner, not disclosure of `DOCKERHUB_PAT` or `APP_PRIVATE_KEY`. It is gated
outside the repository, under Settings > Actions > General, and there is deliberately no fork
guard in any workflow ([ADR 0005](../adr/0005-fork-pull-requests-execute-on-the-self-hosted-runners.md);
the comment above `on:` in [`release.yml`](../../.github/workflows/release.yml) says the same).
The self-hosted runner image ships no `make`, so every job that enters the Makefile first runs
`sudo apt-get install -y build-essential`.

## Test and Release

[`.github/workflows/release.yml`](../../.github/workflows/release.yml), on every pull request to
`main`, every push to `main`, and `workflow_dispatch`. Concurrency-grouped per workflow and ref with
`cancel-in-progress: true`; top-level `permissions: contents: read`, raised only in the two jobs
that need more. `env`: `GO_VERSION: '1.27.1'`, `KUBERNETES_VERSION: '1.33.4'`.

| Job (status context) | Runs | Notes |
|---|---|---|
| `linter` (Code Linting) | `make lint` | |
| `gosec` (GoSec Security Scan) | `make gosec` | 15-minute job timeout, 10-minute step timeout. |
| `malware-scan` (Malware Scan (Source Code)) | ClamAV over the tree, `.git`, `node_modules` and `vendor` excluded | Fails unless the report says `Infected files: 0`; uploads the report for 30 days. |
| `unit-tests` (Unit Tests) | `make test-unit-coverage` | Uploads `coverage/unit.out` for 7 days. |
| `mosquitto-image-tools` (Mosquitto Image Tools) | `make test-image-tools` | Logs in to Docker Hub first, because it pulls the pinned broker image on every run and anonymous pulls are rate-limited. 15-minute timeout. |
| `govulncheck` (Vulnerability Check) | `make vuln` | |
| `cyclomatic-complexity` (Cyclomatic Complexity) | `make cyclo` | Then a summary step that runs `gocyclo -top 20 .` from `PATH` — not `bin/gocyclo`, not `make cyclo-report` — under `\|\| true`. Whether the runner has a `gocyclo` on `PATH` is not visible from the tree; if not, that section of the summary is empty and nothing fails. |
| `generated-manifests` (Generated Manifests Up To Date) | `make generate-all`, a dirty-tree check, then `make verify-rbac-parity` | Installs Helm `v4.3.0`. The dirty check fails on `git diff` or on any untracked file. |
| `integration-tests` (Integration Tests (envtest)) | `make test-integration-coverage` | Uploads `coverage/integration.out` for 7 days. |
| `coverage-report` (Combined Coverage Report) | `needs: [unit-tests, integration-tests]`, runs when at least one succeeded | Merges the profiles, writes the job summary and a sticky PR comment (per-package table, difference to the badge on `main`); on a push to `main` writes the badge and uploads it as the `coverage-badge` artefact ([build-test-lint.md](build-test-lint.md#coverage)). `pull-requests: write`. |
| `container-malware-scan` (Container Malware Scan) | `needs: [malware-scan]` | Builds the image with `persist-credentials: false` on the checkout, **logs out of Docker Hub**, then runs Trivy twice — table at `CRITICAL,HIGH` with `exit-code: 1`, then SARIF. Trivy is pinned to a commit SHA because it used to track a moving branch two steps after a registry login. |
| `release-tooling` (Release Tooling) | `make verify-ci-references`, `npm ci --ignore-scripts`, `npm audit signatures`, `node hack/verify-release-tooling.mjs` | Calls the node script directly rather than `make test-release-tooling`: the Makefile target runs `npm ci --no-audit --no-fund`, the job runs `--ignore-scripts` and checks registry signatures. |
| `semantic-release` (Semantic Release) | only on a push to `main`; `needs:` the twelve jobs above | [The release](#the-release). |

<details>
<summary>The checks that exist, by entry point</summary>

| Check | Entry point | Job |
|---|---|---|
| Lint (`go vet`, `gofmt -l`, golangci-lint with gofmt/goimports) | `make lint` | `linter` |
| gosec | `make gosec` | `gosec` |
| govulncheck | `make vuln` | `govulncheck` |
| gocyclo over threshold 15 | `make cyclo` | `cyclomatic-complexity` |
| Generated artefacts committed | `make generate-all` + dirty-tree check | `generated-manifests`; repeated in `build.yml` before the chart is packaged |
| RBAC parity of both install paths | `make verify-rbac-parity` | last step of `generated-manifests` |
| Renovate customManagers still match | `make verify-ci-references` | first step of `release-tooling` |
| Release notes still render | `node hack/verify-release-tooling.mjs` | `release-tooling` |
| Unit and integration coverage, badge, PR comment | `make test-unit-coverage`, `make test-integration-coverage` | `unit-tests`, `integration-tests`, `coverage-report` |
| The pinned image contains what this repository executes | `make test-image-tools` | `mosquitto-image-tools` |
| ClamAV source scan, Trivy container scan | in-workflow | `malware-scan`, `container-malware-scan` |
| E2E, two legs | `make test-e2e` | `e2e-tests` → `e2e-gate` — **commented out**, below |

</details>

### The E2E jobs are commented out

The `e2e-tests` matrix and the `e2e-gate` job sit at the top of the `jobs:` block **as comments**,
disabled on 2026-09-01. The comment above them records why: a multi-node leg died after ten minutes
inside `sudo apt-get install build-essential`, before it reached a test — an ephemeral runner pod
losing its network, not a defect of this repository. They are commented out rather than skipped
with `if: false` because a skipped matrix would make the gate's `[ "$result" = "success" ]` fail,
and GitHub skips every job whose `needs` were skipped, so `semantic-release` would stop too.
`e2e-tests` is likewise removed from `semantic-release`'s `needs:` (marked `TEMPORARILY REMOVED`).

**To restore:** uncomment the block and add `e2e-tests` back to that `needs:` list. The same
comment notes that `main` carries no branch protection today; if a required status check named
`E2E Tests` is ever configured, this block has to come back first. That branch-protection state is
a repository setting the tree cannot show — not verified here.

What the disabled job does, as written — read it before restoring it, because each step exists for
a mechanic of Docker-in-Docker runners that still applies:

- a matrix of the two legs ([testing.md](testing.md#the-two-legs)), `fail-fast: false`, 60-minute
  timeout, distinct cluster names per leg;
- kernel modules and sysctls for kube-proxy's iptables mode, and a Kind config with
  `kubeProxyMode: iptables` and the `native` containerd snapshotter;
- a node-count check that fails when the cluster came up with fewer nodes than the leg asked for;
- per-node inotify limits, because since Linux 5.13 they are user-namespace-aware and host settings
  are not inherited;
- a resilient DaemonSet wait that collects logs and deletes crash-looping kube-proxy or kindnet
  pods, and an in-cluster connectivity probe that restarts kube-proxy when the ClusterIP DNAT rules
  have not landed yet;
- `make docker-build IMG=mosquitto-operator:test` imported into every node with `ctr`, and the
  pinned broker image preloaded the same way — its tag read out of `test/testimages/images.go`
  with `sed`, never written out again, because a lagging second copy would preload an image the
  suite no longer provisions;
- cert-manager `v1.17.2` and the issuer chain, with a retry loop that fails explicitly when the
  ClusterIssuer cannot be created;
- `helm install` with `test/e2e/helm-values.yaml`, `make test-e2e` tee'd to
  `tmp/e2e-<topology>.log`, and on the filtered leg the grep for
  `--- PASS: TestE2E_AntiAffinity_HardSpreadsAcrossNodes`;
- on failure, operator logs (2000 lines), node conditions, and per `e2e-*` namespace the pods, the
  events and both current and previous container logs; the cluster is deleted `if: always()`.

`e2e-gate`, named `E2E Tests`, would carry the stable status context: a matrix leg reports as
`E2E Tests (<topology>)`, renamed whenever a leg is, so a required check never points at a leg
([ADR 0004](../adr/0004-two-e2e-legs-and-no-version-matrix.md)).

## The release

`semantic-release` runs on a push to `main`, after all twelve jobs above succeeded. Its first step
mints a GitHub App installation token (`actions/create-github-app-token`, from `APP_CLIENT_ID` and
`APP_PRIVATE_KEY`, scoped to this repository with `contents: write`, valid one hour, revoked when
the job ends). Not `GITHUB_TOKEN`, because a release that token creates does not trigger
`build.yml`. The checkout uses `persist-credentials: false`; the coverage badge artefact is
downloaded into `.github/badges/` (the committed file stays when it is missing); `npm ci
--ignore-scripts` and `npm audit signatures`, because this job holds the token; then
`npx semantic-release` with the token as `GITHUB_TOKEN`.

[`.releaserc.json`](../../.releaserc.json): branch `main`; `@semantic-release/commit-analyzer`
with the `conventionalcommits` preset; `@semantic-release/release-notes-generator` loading
[`hack/changelog-config.mjs`](../../hack/changelog-config.mjs), which renders
[`.github/release-template.hbs`](../../.github/release-template.hbs); `@semantic-release/github`
with success and fail comments and released labels off; `@semantic-release/git` committing
`.github/badges/coverage.json` as `chore(release): <version> [skip ci]` with the notes in the body.
The version follows the Conventional Commits since the last tag — a `!` or a `BREAKING CHANGE:`
footer is a major ([conventions.md](conventions.md)).

The release-notes template states that a release is cut only after "unit, integration and E2E
tests" went green. While the E2E jobs are commented out, that sentence is not true.

## Release Docker & Helm

[`.github/workflows/build.yml`](../../.github/workflows/build.yml), on `release: published`.
Serialised by a workflow-wide concurrency group with `cancel-in-progress: false`, because two runs
would both rebase the same `gh-pages` `index.yaml`. `env`: `GO_VERSION: '1.27.1'`.

1. **`build` (Build Docker Image)** — `contents: write` only. Checkout with
   `persist-credentials: false` (the `Containerfile` does `COPY . .`, and
   [`.dockerignore`](../../.dockerignore) excludes `.git/` as the second half). QEMU, buildx
   (`moby/buildkit:v0.12.0`), Docker Hub login; `docker/metadata-action` tags `{{version}}`,
   `{{major}}.{{minor}}`, `{{major}}` and the commit sha — its `latest` entry is gated on
   `github.ref == refs/heads/main`, which a `release` run never matches, so this workflow does not
   move `latest`. Builds and pushes `guidedtraffic/mosquitto-operator` for `linux/amd64` only (arm64
   is commented out), with `provenance: true` and `sbom: true` and the build args `BUILD_NUMBER`
   (the tag without `v`), `GIT_COMMIT`, `BUILD_TIME`; the GitHub Actions build cache stays off. Then
   an SPDX SBOM uploaded to the release, and a Docker Scout CVE scan.
2. **`release-helm-gh` (Release Helm Chart to GitHub Pages)** — `needs: build`, `contents: write`.
   Helm `v4.3.0`; `make generate-all` and the same dirty-tree check as `generated-manifests`, so a
   released chart cannot carry a stale CRD; the release version stamped into `version` and
   `appVersion` of `Chart.yaml` and `image.tag` of `values.yaml` (in the job's checkout, never
   committed to `main`); `helm package`; the index merged with the existing `index.yaml` on
   `gh-pages`; committed and pushed — deliberately **not** forced, so a non-fast-forward rejection
   (somebody published in between) fails the job instead of overwriting their entry; the `.tgz`
   and `index.yaml` attached to the release.

Whether a release has ever been cut is not visible from the tree: the chart in `main` carries
`0.1.0`, and the local clone holds no tags.

## Renovate

[`.github/workflows/renovate.yml`](../../.github/workflows/renovate.yml) runs self-hosted Renovate
daily at 02:00 Europe/Berlin — the `timezone` key is what makes that true; without it GitHub reads
cron as UTC — and on `workflow_dispatch` with a log-level input. **Not on push to `main`**: with
`prConcurrentLimit: 0` and `prHourlyLimit: 0` one run opens every update at once, each queueing a
full Test and Release on the shared runners; the workflow comment records fourteen such pull
requests on 2026-09-01. It authenticates with its own GitHub App installation token, minted in the
first step with every permission of the app, scoped to this repository, valid one hour and revoked
when the job ends.

[`renovate.json`](../../renovate.json), in short:

- **Automerge** (`platformAutomerge`, squash) for minor and patch updates of every manager — Go
  modules, the Go version, Docker base images, GitHub Actions, Helm, the custom-regex pins — and
  for digest updates (the `:automergeDigest` preset and the per-manager rules), after CI.
- **Every major waits for a human**, labelled `major-update`. Majors of *indirect* Go modules are
  disabled outright: a module-path bump cannot apply without a direct importer, and `go mod tidy`
  would keep resetting it. GitHub Actions majors are read before they merge because those actions
  execute on the self-hosted runners in jobs that hold `DOCKERHUB_PAT`, or `APP_PRIVATE_KEY` and
  the token minted from it.
- **Groups:** `Go version` (the `golang-version` datasource and `golang.org/x/*`, so the four Go
  version sites move in one PR — [ADR 0003](../adr/0003-the-go-version-is-one-fact-in-four-files.md)),
  and `Kubernetes Go modules` (`k8s.io/*`, `sigs.k8s.io/*`).
- **`eclipse-mosquitto` stays below 3** (`allowedVersions: "<3"`,
  [ADR 0007](../adr/0007-one-broker-image-pin-and-why-not-the-openssl-tag.md)).
- **Commit types:** `fix` for minor, patch, digest and pin updates, `chore` for every GitHub
  Actions update, so each lands in the right release-notes section.
- **Six customManagers:** the Makefile tool pins, the Go version in `Containerfile`, in `go.mod`,
  in `GO_VERSION` of every workflow (which also selects `renovate.yml`, a tolerated zero) and in
  the release-template badge (`loose` versioning, because the badge carries `1.27`), and the broker
  image in `test/testimages/images.go` and `internal/builder/statefulset.go`. A customManager whose
  regex matches nothing fails silently in Renovate; `make verify-ci-references` is what catches it.
