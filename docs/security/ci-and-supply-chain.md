# CI, release and supply chain

Where the build and release pipeline runs, which credentials it holds and which job holds each, what
a pull request from a fork can make it execute, and what is checked before an image or a chart is
published. What the published operator is allowed to do on a cluster is
[privilege-footprint.md](privilege-footprint.md); what it does with credentials on a cluster is
[credentials.md](credentials.md).

## Where the pipeline runs

Three workflows, eighteen jobs today, and every job runs on `self-hosted` runners — there is no
hosted runner anywhere ([ADR 0005](../adr/0005-fork-pull-requests-execute-on-the-self-hosted-runners.md)
D1):

| Workflow | Triggers | Jobs |
|---|---|---|
| [`release.yml`](../../.github/workflows/release.yml) | `push` to `main`, `pull_request` to `main`, `workflow_dispatch` | 15: the checks, the two E2E legs of `e2e-tests` and their gate `e2e-gate`, `coverage-report`, `container-malware-scan`, `release-tooling`, `semantic-release` |
| [`build.yml`](../../.github/workflows/build.yml) | `release: published` | `build` (image push, SBOM, Scout), `release-helm-gh` (chart to `gh-pages`) |
| [`renovate.yml`](../../.github/workflows/renovate.yml) | `schedule` (02:00 Europe/Berlin), `workflow_dispatch` | `renovate` |

Only `release.yml` runs for a pull request, fork or not; `semantic-release` inside it carries
`if: github.event_name == 'push' && github.ref == 'refs/heads/main'`, so it runs for none
([ADR 0005](../adr/0005-fork-pull-requests-execute-on-the-self-hosted-runners.md) D5), and it waits
on the thirteen jobs before it, `e2e-tests` among them. The E2E jobs were commented out from
2026-09-01 until 2026-10-05 ([ADR 0004](../adr/0004-two-e2e-legs-and-no-version-matrix.md)
`Status`).

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
| `DOCKERHUB_PAT` | `release.yml`: `e2e-tests`, `mosquitto-image-tools` and `container-malware-scan`, each in a `docker/login-action@v4` step. `build.yml`: `build` — its login, and as the `dockerhub-password` input of `docker/scout-action@v1` | Authenticated Docker Hub pulls (the anonymous rate limit is why the image-tools and E2E jobs log in at all); in `build`, the release push and the Scout scan | `container-malware-scan` runs `docker logout` **before** its two Trivy steps, so that third-party code runs with no credential in `~/.docker/config.json`, and Trivy is pinned to a commit. `build` runs `docker logout` right after the push, before `anchore/sbom-action` and `softprops/action-gh-release` run; the Scout step then receives the credential as its own input. `mosquitto-image-tools` and `e2e-tests` keep the credential on disk while the repository's own code runs — `make test-image-tools`, and in `e2e-tests` the image build, the cluster setup and the whole suite |
| `APP_CLIENT_ID`, `APP_PRIVATE_KEY` — organisation secrets, the client ID and private key of the org GitHub App `guided-traffic-automation` | `release.yml`: `semantic-release`. `renovate.yml`: `renovate`. In both only as `with:` inputs of the first step, `actions/create-github-app-token@v3`; no later step receives them as an input or in its environment | Identifying the app and signing the request for an installation token | The private key is the long-lived secret of this set: it does not expire, and per [ADR 0005](../adr/0005-fork-pull-requests-execute-on-the-self-hosted-runners.md)'s amendment it can mint tokens for every repository of the organisation, with every permission the app holds — the app's installations and permissions are not visible from this tree. What this repository controls is where the key goes: neither job runs for a pull request, and the key is handed to the token step alone. It still passes through the self-hosted runner ([H-8](#h-8)). Rotation — a new key in the app settings, then the secret replaced — is manual and outside this repository |
| The GitHub App installation token (`steps.app-token.outputs.token`), minted per job | `semantic-release`: the checkout `token:` and the `GITHUB_TOKEN` env of the Release step. `renovate`: the `token:` input of the Renovate step | Tagging, publishing the release, committing the coverage badge; Renovate's pull requests. Not the job token, because events that token creates start no other workflow — a release it published would never run `build.yml` | Scoped to this repository (the token step sets no `owner` or `repositories`), valid for one hour and revoked by the action's post step, per the workflow comments. `semantic-release` requests `permission-contents: write` only, and [`.releaserc.json`](../../.releaserc.json) turns off `successComment`, `failComment` and `releasedLabels`, so `@semantic-release/github` writes no issue and no comment. `renovate` requests no `permission-*` input and therefore gets every permission of the app installation. In `semantic-release` the checkout sets `persist-credentials: false`, so the token is not written into `.git/config`, and `npm ci --ignore-scripts` followed by `npm audit signatures` installs and checks the release toolchain before the token is in any step's environment — the Release step then runs `npx semantic-release`, the whole installed dependency tree, with the token in its environment |
| `GITHUB_TOKEN`, the job token | `release.yml`: `coverage-report` (its checkout, and the sticky pull-request comment); `semantic-release` holds one too. `build.yml`: `build` (the SBOM upload to the release), `release-helm-gh` (its checkouts, the `git push` to `gh-pages`, the release-asset upload) | The pull-request coverage comment, release assets, publishing the Helm chart | All three workflows set a top-level `permissions: contents: read` floor. In `release.yml` one job raises it, `coverage-report` to `pull-requests: write`; `semantic-release` holds only the floor, because every write it makes goes through the app token. In `build.yml` both jobs raise it to `contents: write` in their own block |

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
`npm audit signatures` over the release toolchain, and the two E2E legs on Kind clusters.
`semantic-release` runs only after the thirteen jobs before it succeeded, `e2e-tests` among them. A
published release then builds and pushes the image with provenance and an SBOM attestation and
runs a Docker Scout CVE scan with no `exit-code` input. The release workflow has run for every
release from `v0.1.0` to `v0.1.8`; the individual checks were not inspected run by run for this
page.

## What this does not cover

<a id="h-7"></a>
### H-7 — A fork pull request executes fork-authored code on the self-hosted runners

Live, and accepted by the maintainer on 2026-09-01
([ADR 0005](../adr/0005-fork-pull-requests-execute-on-the-self-hosted-runners.md)). Stated with the
correct facts, because the wrong version of this sentence is the common one: **secrets are not the
exposure** ([What a fork run receives](#what-a-fork-run-receives)); **code execution on the runner
is**. The workflow's own steps assume unprompted `sudo` — eleven of the fifteen jobs of
`release.yml` install their tools with `sudo apt-get`, and `e2e-tests` also runs `sudo modprobe`
and `sudo sysctl` — and three of them a Docker daemon, both root-equivalent on the host. Under a
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

Live, and narrower than it was. Every third-party action outside the `actions/*`, `docker/*`,
`azure/*` and `helm/*` namespaces is pinned to a commit with its version as a comment —
`aquasecurity/trivy-action` and `marocchino/sticky-pull-request-comment` in `release.yml`,
`anchore/sbom-action` and `softprops/action-gh-release` in `build.yml`,
`renovatebot/github-action` in `renovate.yml` — so a moved tag or branch upstream no longer
reaches the runners on its own. What is left: [`renovate.json`](../../renovate.json) automerges
GitHub Actions minor, patch and digest updates once CI passes, so a new upstream release of a
pinned action still lands after one Renovate cycle without a human reading it; and the four
first-party and vendor namespaces stay on tag references, trusted because their tags are
protected upstream — `docker/login-action@v4` in jobs that then hold `DOCKERHUB_PAT`, and
`actions/create-github-app-token@v3`, the one step that receives `APP_PRIVATE_KEY` itself. The
actions run in jobs that hold `DOCKERHUB_PAT`, or `APP_PRIVATE_KEY` and the app token minted from
it. Taking GitHub Actions out of automerge is what would turn every upstream change into a
reviewed pull request; that was not chosen.

<a id="h-13"></a>
### H-13 — The E2E tier runs on Kind, not on a production cluster

Live, and it bounds every claim on these pages about what a pod, the kubelet, admission or the
garbage collector does. The unit tier runs against controller-runtime's fake client and the
integration tier against envtest, which runs no kubelet and no garbage collector: no broker pod
starts in either, no Secret is mounted or parsed, no MQTT connection is made. The E2E suite under
[`test/e2e/`](../../test/e2e) is the tier that starts a broker, speaks MQTT to it, serves
cert-manager material and watches the owned objects collected
([ADR 0004](../adr/0004-two-e2e-legs-and-no-version-matrix.md)); it runs on every pull request
and gates every release, on Kind clusters the runners create, and was first observed passing on
2026-10-05. A Kind cluster is one container per node with the default CNI and no admission
configuration of its own, so a claim that depends on a CNI, a cloud load balancer, a storage
driver or a production admission chain is not covered by it. What an operator can do: run
`make e2e-local` against a cluster shaped like theirs before relying on one.
