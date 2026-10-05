# CI, release and supply chain

Where the build and release pipeline runs, which credentials it holds and which job holds each, what
a pull request from a fork can make it execute, and what is checked before an image or a chart is
published. What the published operator is allowed to do on a cluster is
[privilege-footprint.md](privilege-footprint.md); what it does with credentials on a cluster is
[credentials.md](credentials.md).

## Where the pipeline runs

Three workflows, sixteen jobs today, and every job runs on `self-hosted` runners — there is no
hosted runner anywhere ([ADR 0005](../adr/0005-fork-pull-requests-execute-on-the-self-hosted-runners.md)
D1):

| Workflow | Triggers | Jobs |
|---|---|---|
| [`release.yml`](../../.github/workflows/release.yml) | `push` to `main`, `pull_request` to `main`, `workflow_dispatch` | 13: the checks, `coverage-report`, `container-malware-scan`, `release-tooling`, `semantic-release` |
| [`build.yml`](../../.github/workflows/build.yml) | `release: published` | `build` (image push, SBOM, Scout), `release-helm-gh` (chart to `gh-pages`) |
| [`renovate.yml`](../../.github/workflows/renovate.yml) | `schedule` (02:00 Europe/Berlin), `workflow_dispatch` | `renovate` |

Only `release.yml` runs for a pull request, fork or not; `semantic-release` inside it carries
`if: github.event_name == 'push' && github.ref == 'refs/heads/main'`, so it runs for none
([ADR 0005](../adr/0005-fork-pull-requests-execute-on-the-self-hosted-runners.md) D5). The E2E jobs
`e2e-tests` and `e2e-gate` are commented out of `release.yml` since 2026-09-01, and
`semantic-release` waits on the twelve jobs that remain ([H-13](#h-13)).

## What a fork run receives

For a `pull_request` run from a fork of this public repository, GitHub passes no repository or
organisation secret — `secrets.DOCKERHUB_PAT`, `secrets.APP_CLIENT_ID` and `secrets.APP_PRIVATE_KEY`
are empty strings, so no app token can be minted — and the job token is read-only
([ADR 0005](../adr/0005-fork-pull-requests-execute-on-the-self-hosted-runners.md) D4). That is
GitHub's documented behaviour, reasoned from its documentation and not reproduced here. What a fork
run does get is code execution on the fleet ([H-7](#h-7)).

## Credentials in CI

| Credential | Which job holds it | What for | What bounds it |
|---|---|---|---|
| `DOCKERHUB_PAT` | `release.yml`: `mosquitto-image-tools` and `container-malware-scan`, each in a `docker/login-action@v4` step. `build.yml`: `build` — its login, and as the `dockerhub-password` input of `docker/scout-action@v1` | Authenticated Docker Hub pulls (the anonymous rate limit is why the image-tools job logs in at all); in `build`, the release push and the Scout scan | `container-malware-scan` runs `docker logout` **before** its two Trivy steps, so that third-party code runs with no credential in `~/.docker/config.json`, and Trivy is pinned to a commit (`aquasecurity/trivy-action@ed142fd…`). `mosquitto-image-tools` keeps the credential on disk while `make test-image-tools` runs. `build` logs out nowhere: the credential stays on disk through `anchore/sbom-action@v0` and `softprops/action-gh-release@v3`; that job runs only for a published release |
| `APP_CLIENT_ID`, `APP_PRIVATE_KEY` — organisation secrets, the client ID and private key of the org GitHub App `guided-traffic-automation` | `release.yml`: `semantic-release`. `renovate.yml`: `renovate`. In both only as `with:` inputs of the first step, `actions/create-github-app-token@v3`; no later step receives them as an input or in its environment | Identifying the app and signing the request for an installation token | The private key is the long-lived secret of this set: it does not expire, and per [ADR 0005](../adr/0005-fork-pull-requests-execute-on-the-self-hosted-runners.md)'s amendment it can mint tokens for every repository of the organisation, with every permission the app holds — the app's installations and permissions are not visible from this tree. What this repository controls is where the key goes: neither job runs for a pull request, and the key is handed to the token step alone. It still passes through the self-hosted runner ([H-8](#h-8)). Rotation — a new key in the app settings, then the secret replaced — is manual and outside this repository |
| The GitHub App installation token (`steps.app-token.outputs.token`), minted per job | `semantic-release`: the checkout `token:` and the `GITHUB_TOKEN` env of the Release step. `renovate`: the `token:` input of the Renovate step | Tagging, publishing the release, committing the coverage badge; Renovate's pull requests. Not the job token, because events that token creates start no other workflow — a release it published would never run `build.yml` | Scoped to this repository (the token step sets no `owner` or `repositories`), valid for one hour and revoked by the action's post step, per the workflow comments. `semantic-release` requests `permission-contents: write` only, and [`.releaserc.json`](../../.releaserc.json) turns off `successComment`, `failComment` and `releasedLabels`, so `@semantic-release/github` writes no issue and no comment. `renovate` requests no `permission-*` input and therefore gets every permission of the app installation. In `semantic-release` the checkout sets `persist-credentials: false`, so the token is not written into `.git/config`, and `npm ci --ignore-scripts` followed by `npm audit signatures` installs and checks the release toolchain before the token is in any step's environment — the Release step then runs `npx semantic-release`, the whole installed dependency tree, with the token in its environment |
| `GITHUB_TOKEN`, the job token | `release.yml`: `coverage-report` (its checkout, and the sticky pull-request comment); `semantic-release` holds one too. `build.yml`: `build` (the SBOM upload to the release), `release-helm-gh` (its checkouts, the `git push` to `gh-pages`, the release-asset upload) | The pull-request coverage comment, release assets, publishing the Helm chart | `release.yml` and `renovate.yml` set a top-level `permissions: contents: read` floor; two jobs raise it in their own block — `coverage-report` to `pull-requests: write`, `semantic-release` to `contents`, `issues`, `pull-requests` and `id-token` write. `build.yml` has no top-level floor; both its jobs declare `contents: write` ([H-10](#h-10)) |

## Keeping the job token out of the image

The `Containerfile` does `COPY . .`, and two mechanisms keep a job token out of the build context —
the comments in both files say plainly that either one alone would be a single point of failure:
[`.dockerignore`](../../.dockerignore) excludes `.git/`, **and** the two jobs that build an image,
`container-malware-scan` and `build`, check out with `persist-credentials: false`
([ADR 0005](../adr/0005-fork-pull-requests-execute-on-the-self-hosted-runners.md) D8).

## What is checked before anything is published

On every push and pull request `release.yml` runs `gosec`, `govulncheck`, a ClamAV scan of the
source tree, a Trivy scan of an image built from the tree (vulnerabilities, secrets and
misconfigurations; a `CRITICAL` or `HIGH` finding fails the job, a vulnerability without a fix is
ignored), the regeneration of every generated artefact with a dirty-tree check followed by the
RBAC parity check ([privilege-footprint.md](privilege-footprint.md#two-install-paths-one-authority)),
and `npm audit signatures` over the release toolchain. `semantic-release` runs only after all twelve
other jobs of `release.yml` succeeded. A published release then builds and pushes the image with
provenance and an SBOM attestation and runs a Docker Scout CVE scan with no `exit-code` input. None
of these checks was observed in a workflow run for this page.

## What this does not cover

<a id="h-7"></a>
### H-7 — A fork pull request executes fork-authored code on the self-hosted runners

Live, and accepted by the maintainer on 2026-09-01
([ADR 0005](../adr/0005-fork-pull-requests-execute-on-the-self-hosted-runners.md)). Stated with the
correct facts, because the wrong version of this sentence is the common one: **secrets are not the
exposure** ([What a fork run receives](#what-a-fork-run-receives)); **code execution on the runner
is**. The workflow's own steps assume unprompted `sudo` — ten of the thirteen jobs of
`release.yml` install their tools with `sudo apt-get` — and two of them a Docker daemon, both
root-equivalent on the host. Under a
`pull_request` trigger the workflow definition itself comes from the pull request (GitHub
behaviour, not reproduced here), so no file of the repository is out of a fork author's reach. The
control is a repository setting, not a file: *Settings > Actions > General > "Fork pull request
workflows from outside collaborators" = "Require approval for all outside collaborators"*, asserted
by the comment at the top of `release.yml`. **This repository cannot see that setting, and this
page does not claim it is set**; if it is not, the control does not exist. For a maintainer,
approving a run means having read the diff of everything a job executes —
[`.github/workflows/`](../../.github/workflows), the [`Containerfile`](../../Containerfile), the
[`Makefile`](../../Makefile), the scripts under [`hack/`](../../hack) and the test code.

<a id="h-8"></a>
### H-8 — Runner isolation is asserted, not verified

Open, and it is the path from code execution to secrets. The comments in `release.yml` describe
each job landing on "its own ephemeral ARC runner pod"; that is infrastructure this repository
cannot inspect. If the runners are not ephemeral, fork-authored code ([H-7](#h-7)) can leave state —
a poisoned build or module cache, a modified `~/.docker/config.json`, a scheduled process — that a
later trusted job executes with `DOCKERHUB_PAT` on disk, or while `semantic-release` or `renovate`
hands `APP_PRIVATE_KEY` to its token step. The key is the worse catch: the token lives an hour and
reaches this repository only, the key does not expire and mints tokens for every repository the app
is installed on. Within one job every step runs on the same runner; what a later step's code can
recover of what an earlier step received was not examined here. What a maintainer can do: confirm
outside this repository that every job gets a fresh runner, and treat any doubt about it as a
reason to rotate the app key.

<a id="h-9"></a>
### H-9 — Action code changes without review and runs beside the credentials

Live. [`renovate.json`](../../renovate.json) automerges GitHub Actions minor, patch and digest
updates once CI passes; majors need review. Every action runs on the fleet, in jobs that hold
`DOCKERHUB_PAT`, or `APP_PRIVATE_KEY` and the app token minted from it. Only
`aquasecurity/trivy-action` is pinned to a commit; every other action is referenced by tag, and a
tag such as `@v0` or `@v3` moves with every upstream release without any pull request at all. The
comment above the Trivy step argues that the tag-referenced actions — it names `actions/*`,
`docker/*`, `azure/*` and `helm/*` — are first-party or vendor-published with protected tags. Four
actions outside those namespaces run on tag references too: `anchore/sbom-action@v0` and
`softprops/action-gh-release@v3` in `build.yml`, after the Docker Hub login and with a
`contents: write` job token; `marocchino/sticky-pull-request-comment@v3` in `coverage-report`, with
`pull-requests: write`; and `renovatebot/github-action@v46.3.6` in `renovate`, with an app token
carrying every permission of the installation. `actions/create-github-app-token@v3`, a tag
reference, is the one step that receives the private key itself. Dropping `digest` from the
automerge rule covers only the one action that is pinned; pinning every action to a commit and
taking GitHub Actions out of automerge is what turns an upstream change into a reviewed pull
request.

<a id="h-10"></a>
### H-10 — `build.yml` has no top-level `permissions:` floor

Dormant. Both current jobs of [`build.yml`](../../.github/workflows/build.yml) declare
`contents: write` themselves, so the file is correct as it stands; a job added without a block of
its own inherits the repository's default token permissions, which may be read and write on every
scope — that default is a repository setting and not visible from this tree. `release.yml` and
`renovate.yml` set `permissions: contents: read` at the top and do not have this problem. One line
closes it ([ADR 0005](../adr/0005-fork-pull-requests-execute-on-the-self-hosted-runners.md) D7).

<a id="h-13"></a>
### H-13 — Nothing here has been observed on a real cluster, and the tier that would observe it is out of CI

Live, and it bounds every claim on these pages about what a pod, the kubelet, admission or the
garbage collector does. The unit tier runs against controller-runtime's fake client and the
integration tier against envtest, which runs no kubelet and no garbage collector: no broker pod
starts in either, no Secret is mounted or parsed, no MQTT connection is made. The E2E suite under
[`test/e2e/`](../../test/e2e) is the only tier that starts a broker, speaks MQTT to it, serves
cert-manager material and watches the owned objects collected
([ADR 0004](../adr/0004-two-e2e-legs-and-no-version-matrix.md)); its jobs are commented out of
`release.yml` since 2026-09-01, and no run of it has been observed. Every such claim on these pages
says it was not observed. What an operator can do: run `make e2e-local` against a Kind cluster
before relying on one.
