# Build, test and lint

Every Make target, what each needs and what it proves; the tool pins; the generated code; what
was recorded the last time the matrix was run by hand; and the two coverage pipelines. The test
tiers themselves — what each can answer, the fixtures, the E2E legs — are
[testing.md](testing.md); the CI jobs that call these targets are
[ci-and-release.md](ci-and-release.md). Read against the tree on 2026-10-05.

**The [`Makefile`](../../Makefile) is the entry point.** CI enters the repository only through
Make targets, so a local result and a CI result are comparable. `make help` prints the grouped
target list.

## Prerequisites

| Tool | Version | Where the version lives | Needed for |
|---|---|---|---|
| Go | `1.27.1` | `go 1.27.1` in [`go.mod`](../../go.mod), `golang:1.27.1-alpine` in the [`Containerfile`](../../Containerfile), `GO_VERSION: '1.27.1'` in [`release.yml`](../../.github/workflows/release.yml) and [`build.yml`](../../.github/workflows/build.yml), and `go-1.27` (major.minor) in the badge of [`.github/release-template.hbs`](../../.github/release-template.hbs) | everything |
| make + bash | any recent | `SHELL = /usr/bin/env bash -o pipefail`, `.SHELLFLAGS = -ec` | everything |
| Docker | not pinned | — | `test-image-tools`, `docker-build`, `docker-buildx`, `kind-load`, `e2e-local` |
| kubectl | not pinned for local use | `KUBERNETES_VERSION: '1.33.4'` in `release.yml`, used only by the E2E job | `install`, `uninstall`, `deploy`, `undeploy`, `cert-manager-install`, `e2e-local`, and `test-e2e` (the suite runs `kubectl exec`) |
| kind | not pinned for local use | `v0.30.0` through `helm/kind-action@v1.14.0`, in the E2E job | `kind-create`, `kind-delete`, `kind-load`, `e2e-local` |
| helm | not pinned for local use; CI `v4.3.0` | `azure/setup-helm@v5` in the `generated-manifests` job and in `build.yml` | `verify-rbac-parity`, `e2e-local` |
| node + npm | CI `lts/*` | `actions/setup-node@v7` in `release.yml` | `test-release-tooling` (`npm ci`), `verify-ci-references` (node only, no install) |
| git | any | — | `verify-rbac-parity` (the test finds the repository root with `git rev-parse --show-toplevel`) |
| bc | any | — | `coverage-json` |

Everything else installs itself into `bin/` on first use, through `go install` at a version pinned
in the Makefile:

| Binary | Variable | Pinned to | Renovate |
|---|---|---|---|
| `kustomize` | `KUSTOMIZE_VERSION` | `v5.8.2` | `# renovate:` comment |
| `controller-gen` | `CONTROLLER_GEN_VERSION` | `v0.21.0` | `# renovate:` comment |
| `setup-envtest` | `ENVTEST_VERSION` | `release-0.19` — a controller-runtime branch | none, bumped by hand |
| envtest control plane | `ENVTEST_K8S_VERSION` | `1.29.0`, under `bin/k8s/<version>-<os>-<arch>` | none, bumped by hand |
| `golangci-lint` | `GOLANGCI_LINT_VERSION` | `v2.14.0` | `# renovate:` comment |
| `gocyclo` | `GOCYCLO_VERSION` | `v0.6.0` | `# renovate:` comment |
| `gosec` | `GOSEC_VERSION` | `v2.29.0` | `# renovate:` comment |
| `govulncheck` | `GOVULNCHECK_VERSION` | `v1.8.0` | `# renovate:` comment |
| `gocovmerge` | `GOCOVMERGE_VERSION` | `v0.0.0-20160331181800-b5bfa59ec0ad` | none — upstream has no tags |

Three Makefile properties worth knowing before you fight one of them:

- **Every tool resolves to `$(LOCALBIN)` (`./bin`), never to `PATH`.** A tool picked up from `PATH`
  silently ignores the pin — which is how a Homebrew gosec 2.28.0 once ran in place of the pinned
  `v2.29.0`. To try another version, override the variable
  (`make lint GOLANGCI_LINT=$(which golangci-lint)`).
- **A pin that moved does not reinstall the tool.** `go-install-tool` installs only when the file
  is missing (`[ -f $(1) ] || …`), and the binaries carry no version in their names. After a
  Renovate bump of a pin, delete the binary under `bin/` — or all of `bin/` — or the old version
  keeps running locally.
- **`ENVTEST_VERSION` names a branch**, so no Renovate datasource resolves it; it is bumped by hand
  together with `ENVTEST_K8S_VERSION`, and it deliberately carries no `# renovate:` comment,
  because a comment that matches nothing is exactly the failure
  [`hack/verify-ci-references.mjs`](../../hack/verify-ci-references.mjs) exists to catch.

## The fast local loop

None of these needs a cluster or Docker:

```bash
make lint             # go vet + gofmt -l + golangci-lint
make test-unit        # every untagged test
make test-integration # the real reconciler against envtest
make generate-all     # then check that `git status` is clean
```

## Targets

| Target | Needs | What it proves |
|---|---|---|
| `lint` | nothing (installs `golangci-lint`) | `go vet ./...`, `gofmt -l .`, `golangci-lint run --timeout=5m`. `gofmt -l` only lists; formatting is *enforced* by the `gofmt` and `goimports` formatters enabled in [`.golangci.yml`](../../.golangci.yml) (`goimports` with the module as local prefix). |
| `lint-fix` | the same | `golangci-lint run --fix`. |
| `cyclo` | nothing (installs `gocyclo`) | No function over `CYCLO_THRESHOLD` (15), `_test.go` files excluded. |
| `cyclo-report` | the same | `gocyclo -top 20 .`, tests included. |
| `gosec` | nothing (installs `gosec`) | Security scan, bounded to `GOSEC_CONCURRENCY=4` and `GOMEMLIMIT=1GiB`: the unbounded default peaks at about 10 GB on the shared self-hosted runner and gets it OOM-killed. |
| `vuln` | nothing (installs `govulncheck`) | No known vulnerability reachable from this code. |
| `test-unit` | the envtest download on first run | `go test -v ./...` — every untagged test. It declares `envtest` as a prerequisite and exports `KUBEBUILDER_ASSETS`, but no unit test starts a control plane. |
| `test-unit-coverage` | the same | The same, writing `coverage/unit.out` (`-covermode=atomic`). |
| `test-integration` | the envtest binaries | `-tags=integration -count=1 -timeout=60m ./test/integration/...` — the real reconciler against a real API server. |
| `test-integration-coverage` | the same | The same with `-coverpkg=./...`, writing `coverage/integration.out`. |
| `test-e2e` | a running cluster with the operator installed, `kubectl`, a kubeconfig | `-tags=e2e -count=1 -timeout=30m ./test/e2e/...`; `E2E_RUN` adds a `-run` filter, `E2E_MOSQUITTO_IMAGE` is passed through. |
| `test-image-tools` | Docker, no cluster | `-tags=imagetools -count=1 -timeout=15m ./test/imagetools/...`. First run pulls the pinned image. |
| `verify-rbac-parity` | helm, git; installs `bin/kustomize` | `-tags=rbacparity -count=1 ./test/rbacparity/...` — both install paths render the same authority. Must run inside the git worktree. |
| `verify-ci-references` | node, no `npm install` | Every Renovate customManager still matches a real file and line. |
| `test-release-tooling` | node + npm | `npm ci --no-audit --no-fund`, then `node hack/verify-release-tooling.mjs`. |
| `generate-all` | nothing (installs `controller-gen`) | `manifests` + `generate` + `sync-helm-crd` ([below](#generated-code)). Follow it with `git status`. |
| `build` | nothing | `bin/manager` and `bin/exporter`, after `fmt` and `vet` — and `fmt` rewrites files in place. |
| `run` | a kubeconfig | `go run ./cmd/main.go --zap-log-level=debug` against your current cluster, after `fmt` and `vet`. |
| `docker-build` | Docker | The operator image `IMG`. Depends on `generate-all`, so it regenerates first. |
| `kind-create` / `kind-delete` | kind, Docker | A cluster named `KIND_CLUSTER` with one control-plane node and `KIND_WORKERS` workers; the config goes to `tmp/kind-config.yaml`. |
| `kind-load` | kind, Docker | `docker-build`, then `kind load docker-image $(IMG)`. |
| `cert-manager-install` | kubectl, a cluster | Applies cert-manager `v1.17.2` from its release URL, waits for its three Deployments, applies [`test/e2e/testdata/cert-manager-issuer.yaml`](../../test/e2e/testdata/cert-manager-issuer.yaml). |
| `e2e-local` | Docker, kind, kubectl, helm | `kind-create`, `cert-manager-install`, builds and loads `E2E_IMG`, `helm install` with [`test/e2e/helm-values.yaml`](../../test/e2e/helm-values.yaml) into `mosquitto-operator-system`, `test-e2e`, `kind-delete`. A failing `test-e2e` stops the recipe before `kind-delete`, so the cluster stays for inspection. |

`make test-unit` deliberately passes no `-short`, and no test checks `testing.Short()`: CI runs the
coverage targets, so such a gate would remove the tests behind it from every automated run while
they still pass locally.

<details>
<summary>Every other target, by Makefile group</summary>

**General** — `help`, `all` (= `build`).

**Development** — `fmt` (`gofmt -s -w .`, rewrites in place), `vet` (`go vet ./...`), `test`
(`fmt vet envtest`, then `go test ./... -coverprofile cover.out` — the profile lands at the
repository root, not in `coverage/`), `test-coverage` (`test`, then `coverage.html` at the root),
`coverage` (untagged tests into `coverage/coverage.out`, `.html` and `.txt`), `coverage-ci` (the
same without HTML), `coverage-merge` and `coverage-json` ([below](#coverage)).

**Security** — `gosec`, `vuln`.

**Code generation** — `sync-helm-crd`. `manifests` and `generate` sit in the Dependencies group of
the Makefile but are generation targets ([below](#generated-code)).

**Build** — `docker-push` (`docker push $(IMG)`), `docker-buildx` (creates a builder, builds and
**pushes** `linux/amd64` and `linux/arm64`, removes the builder).

**Deployment** — `install` / `uninstall` (`kustomize build config/crd`, then `config/rbac`, applied
with kubectl; reverse order on the way out — and removing the CRD deletes every `Mosquitto` in
the cluster along with the broker workloads they own), `deploy` / `undeploy` (`kustomize build
config/default`). `install` applies `config/rbac` without the `config/default` overlay, so those
objects carry no `mosquitto-operator-` prefix and the namespaced ones land in the literal namespace
`system`; nothing tests that rendering.

**Dependencies** — `kustomize`, `controller-gen`, `envtest`, `golangci-lint` download their tool;
`gocyclo`, `gosec`, `govulncheck` and `gocovmerge` have no alias of their own, because the name
would collide with the target that runs them.

</details>

<details>
<summary>Variables worth overriding</summary>

| Variable | Default | Effect |
|---|---|---|
| `IMG` | `guidedtraffic/mosquitto-operator:latest` | image built, pushed or loaded |
| `E2E_IMG` | `mosquitto-operator:test` | image `e2e-local` builds and loads; matches `test/e2e/helm-values.yaml` |
| `KIND_CLUSTER` | `mosquitto-operator-test` | cluster name |
| `KIND_WORKERS` | `3` | worker nodes `kind-create` adds. The lines come from a counting loop, not `seq`: BSD `seq 1 0` counts *down* and would build a two-worker cluster for `KIND_WORKERS=0` |
| `E2E_RUN` | empty | `-run` filter for `test-e2e` |
| `E2E_MOSQUITTO_IMAGE` | empty | broker image the E2E suite provisions; empty uses the pin in `test/testimages/images.go` |
| `CYCLO_THRESHOLD` | `15` | complexity gate |
| `GOSEC_CONCURRENCY` / `GOSEC_MEMLIMIT` | `4` / `1GiB` | the memory bound described above |
| `ENVTEST_K8S_VERSION` | `1.29.0` | envtest control-plane version |
| `LOCALBIN` | `$(pwd)/bin` | where the tools go |
| `ignore-not-found` | `false` | passed to `kubectl delete` by `uninstall` / `undeploy` |
| tool paths (`GOLANGCI_LINT`, `GOSEC`, …) | `$(LOCALBIN)/<tool>` | run another binary than the pinned one |

**`make deploy` edits a tracked file.** It runs `kustomize edit set image controller=$(IMG)` inside
`config/manager/`, which rewrites `config/manager/kustomization.yaml`. Revert that before
committing. (Read from the target; not executed.)

</details>

## Generated code

`make generate-all` runs:

- `manifests` — `controller-gen rbac:roleName=mosquitto-operator-role crd paths="./..."`, writing
  [`config/crd/bases/mko.gtrfc.com_mosquittoes.yaml`](../../config/crd/bases/mko.gtrfc.com_mosquittoes.yaml)
  and [`config/rbac/role.yaml`](../../config/rbac/role.yaml), then `sync-helm-crd`;
- `generate` — `controller-gen object:headerFile="hack/boilerplate.go.txt"`, writing
  `api/v1/zz_generated.deepcopy.go`;
- `sync-helm-crd` — concatenates every file under `config/crd/bases/` into
  [`deploy/helm/mosquitto-operator/templates/crd.yaml`](../../deploy/helm/mosquitto-operator/templates/crd.yaml)
  behind a "do not edit" header.

Run it after any change under `api/v1/` or to an RBAC marker, and commit what it writes. Never
edit a generated file: the `generated-manifests` job runs the generator and fails on a dirty tree,
untracked files included, and `build.yml` repeats the same check before it packages the chart.
The chart's `clusterrole.yaml` is **not** generated; a marker change is mirrored into it by hand
and checked by `make verify-rbac-parity` ([adding-things.md](adding-things.md#a-managed-object)).

## What was recorded

The last full run by hand is the one recorded when the former root `DEVELOPER.md` was written, on
macOS (Apple Silicon) with `go1.27.0 darwin/arm64` and the tool pins of that time — golangci-lint
`v2.13.2`, govulncheck `v1.7.0`, kustomize `v5.8.1` — with warm module and build caches. **It was not
re-run for this page**, and the pins have moved since. Times are wall clock, a lower bound: a cold
clone pays for the tool downloads, the envtest assets and the image pull on top.

| Command | Result | Wall clock |
|---|---|---|
| `make help` | prints the grouped target list | — |
| `make test-unit` | all 6 packages pass (from the Go test cache) | — |
| `make lint` | `0 issues.` | 3.1 s |
| `make cyclo` | `All functions are below complexity threshold 15` | 0.5 s |
| `make gosec` | `Files: 11  Lines: 1623  Nosec: 0  Issues: 0` | 1.4 s |
| `make vuln` | `No vulnerabilities found.` | 2.7 s |
| `GOFLAGS=-count=1 make test-unit-coverage` | all 6 packages pass — `api/v1` 90.0%, `cmd` 36.8%, `internal/builder` 99.0%, `internal/common` 100.0%, `internal/controller` 91.9%, `test/testimages` 100.0% | 6.7 s |
| `make test-integration` | 12 tests pass, `ok ... 9.096s` | 12.1 s |
| `make test-image-tools` | 2 tests pass against `eclipse-mosquitto:2.1.2-alpine` | 1.9 s, image cached |
| `make verify-rbac-parity` | pass | 2.0 s |
| `make verify-ci-references` | `OK: all 6 Renovate customManagers reference real files and lines` | 0.4 s |
| `make test-release-tooling` | `OK: release tooling renders release notes` — `analyzeCommits -> major`, `generateNotes -> 1450 chars, all sections present` | 2.9 s |
| `make generate-all` | no change to the working tree | 4.1 s |
| `make build` | `bin/manager` written (measured before `bin/exporter` was added) | 2.4 s |
| `make docker-build IMG=mosquitto-operator:doccheck` | image built (and removed again afterwards) | 5.5 s, layers cached |

Not run, and therefore not claimed to work: `test`, `test-coverage`, `coverage`, `coverage-ci`,
`coverage-merge`, `coverage-json`, `test-integration-coverage`, `lint-fix`, `run`, `test-e2e`,
`e2e-local`, `kind-create`, `kind-delete`, `kind-load`, `cert-manager-install`, `docker-push`,
`docker-buildx`, `install`, `uninstall`, `deploy`, `undeploy`.

## Coverage

Two pipelines produce a coverage number, and they do not agree on the badge colours:

- **Locally**, `coverage-merge` runs `gocovmerge` over `coverage/unit.out` and
  `coverage/integration.out` into `coverage/combined.out` and `combined.txt`, and `coverage-json`
  turns the total into badge JSON with thresholds 80/60/40/20 (brightgreen/green/yellow/orange,
  else red), computed with `bc`. **`coverage-json` writes the committed
  [`.github/badges/coverage.json`](../../.github/badges/coverage.json)** — revert it before
  committing.
- **In CI**, the `coverage-report` job calls neither target. It downloads the two profile
  artefacts, concatenates them under `mode: set`, and computes the badge in Python with thresholds
  90/80/70/60. For the per-package table it deduplicates blocks by location first, because the
  concatenation makes every block both runs touched appear twice, and under `mode: set` a block is
  covered when either run covered it. The job runs when at least one of the two test jobs
  succeeded, so its number can come from one profile alone; such a run releases nothing, because
  `semantic-release` needs both test jobs.

A badge produced by `make coverage-json` can therefore have a different colour than the one CI
writes for the same percentage. The badge on `main` is the CI one, committed by
`semantic-release` ([ci-and-release.md](ci-and-release.md#the-release)); the committed file reads
`85.2%`, `green` today.
