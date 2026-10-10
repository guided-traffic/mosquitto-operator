# ADR 0010: A check is not a check until it has failed on purpose

## Status

Accepted. Date: 2026-09-01.

**Amended 2026-09-25:** `BOT_PAT` below is now `APP_PRIVATE_KEY`, the private key of the org
GitHub App `guided-traffic-automation`, and the installation token minted from it
([ADR 0005](0005-fork-pull-requests-execute-on-the-self-hosted-runners.md) Status). The
statement is unchanged otherwise.

**Verified by reading, in this tree:**
[`hack/verify-ci-references.mjs`](../../hack/verify-ci-references.mjs),
[`hack/verify-release-tooling.mjs`](../../hack/verify-release-tooling.mjs),
[`hack/changelog-config.mjs`](../../hack/changelog-config.mjs),
[`.releaserc.json`](../../.releaserc.json) (the `@semantic-release/release-notes-generator`
entry carries `"config": "./hack/changelog-config.mjs"`),
[`.github/release-template.hbs`](../../.github/release-template.hbs),
[`test/rbacparity/rbac_parity_test.go`](../../test/rbacparity/rbac_parity_test.go),
[`Makefile`](../../Makefile) lines 131-145 (`test-release-tooling`, `verify-ci-references`,
`verify-rbac-parity`), and the two jobs in
[`.github/workflows/release.yml`](../../.github/workflows/release.yml): `generated-manifests`
(line 742, running `make verify-rbac-parity` at line 792) and `release-tooling` (line 1192,
running `make verify-ci-references` at line 1213 and `node hack/verify-release-tooling.mjs` at
line 1225).

**Verified by running, on 2026-09-01, on this machine (darwin/arm64, `go1.27.0`,
`helm v3.21.3+g1ad6e68`, `bin/kustomize v5.8.1`):**

| What was run | Result |
|---|---|
| `node hack/verify-ci-references.mjs` | exit 0, `OK: all 6 Renovate customManagers reference real files and lines` |
| `node hack/verify-release-tooling.mjs` | exit 0, `analyzeCommits -> major`, `generateNotes -> 1450 chars, all sections present` |
| `generateNotes` with the stock `conventionalcommits` preset instead of `./hack/changelog-config.mjs` | all five preset needles present; `Quality Gates` **absent** |
| `go test -tags=rbacparity` on an exported copy of `HEAD` with `delete` injected into the chart's `statefulsets` rule only | FAIL: `ClusterRole apps/statefulsets: the two install paths grant different verbs`, expected `[create get list update watch]`, actual `[create delete get list update watch]` |
| the same copy with the chart's `clusterrole.yaml` and `leader-election.yaml` removed | FAIL at line 160: `Should NOT be empty, but was map[]`, `parsed no RBAC rules from the Helm output` |

The two failing runs were done in a copy of the tree produced with `git archive HEAD`, outside
the repository; nothing in the working tree was modified.

**Implemented:** three guards, all three runnable without a Kubernetes cluster, and four
recorded observations of a guard failing on purpose — now spread over all three.

**Amended 2026-10-05 — the third guard failed on purpose.** With the seventh customManager (the
BuildKit image of `build.yml`) added, its `matchStrings` entry was edited from
`image=moby/buildkit:(?<currentValue>v[\\d.]+)` to `image=moby/buildkit@(?<currentValue>v[\\d.]+)`
and `make verify-ci-references` run: exit 2, `BAD: customManagers[6] "Update the BuildKit image the
release build runs …"`, `.github/workflows/build.yml (0)`, and `FAIL: 1 Renovate customManager
problem(s) in renovate.json: … matchString "image=moby/buildkit@(?<currentValue>v[\\d.]+)" matches
no line in [".github/workflows/build.yml"]`. Restored, it prints `OK: all 7 Renovate
customManagers reference real files and lines`.

**Amended 2026-10-10 — the template is a footer now, and the second guard failed on purpose
twice more.** `conventional-changelog-conventionalcommits` 10 renders through
`conventional-changelog-writer` 9, which has no handlebars and no `mainTemplate`; with the writer 8
that `@semantic-release/release-notes-generator` 14.1.1 asks for, it rendered the header and empty
`Features` and `Bug Fixes` sections, and `release-tooling` went red on its pull request with `FAIL:
rendered release notes are missing "requeue while the StatefulSet is progressing"`. `package.json`
now overrides that writer with a pinned `9.3.0`, and
[`hack/changelog-config.mjs`](../../hack/changelog-config.mjs) hands
`.github/release-template.hbs` to the preset as its footer partial, filling in its one
placeholder, `{{version}}`. The notes of the synthetic commit set rendered byte-identical to those
of the old toolchain. Two needles now carry the version, so four are template-owned (D3). Broken
on purpose, on darwin/arm64 with node 26.5.0: the footer handed over without filling the
placeholder gave `FAIL: rendered release notes are missing "docker pull
guidedtraffic/mosquitto-operator:2.0.0"`; the override removed, so the generator loaded writer
8.4.0, gave `FAIL: generateNotes threw - the release-notes-generator/preset/writer set is broken`.
The override is a bridge, so the script also fails once the generator itself asks for the pinned
writer's major: with its installed `package.json` edited to `^9.0.0`, `FAIL:
@semantic-release/release-notes-generator 14.1.1 asks for conventional-changelog-writer ^9.0.0,
which the pinned 9.3.0 satisfies - remove the override from package.json`. Restored, `OK: release
tooling renders release notes`.

**Open / not verified:** ~~no job of this pipeline has been observed executing on GitHub~~
*(amended 2026-10-05: the release workflow has run on every push to `main` and built
`v0.1.0` to `v0.1.8`; the individual guard steps were not inspected run by run).* The reproductions
above used `helm v3.21.3` while the `generated-manifests` job installs `v4.2.4` via
`azure/setup-helm@v5`, so the rendering half of the parity test has not been exercised with the
helm major CI uses. Nothing in this repository has ever run against a real cluster.

## Context

Every check in this pipeline is a claim: "if X breaks, this goes red." The claim is cheap to
write and expensive to believe, because the failure mode of a check is not to crash — it is to
stay green over a broken input. A guard that cannot fail costs a runner slot on every pull
request and buys nothing, and it is worse than absent, because its green tick is read as
evidence.

This repository has three guards whose whole purpose is to catch a silent break:

* [`hack/verify-ci-references.mjs`](../../hack/verify-ci-references.mjs) — a Renovate
  `customManager` whose regex stops matching contributes no dependency and reports no error
  ([ADR 0003](0003-the-go-version-is-one-fact-in-four-files.md)).
* [`hack/verify-release-tooling.mjs`](../../hack/verify-release-tooling.mjs) — the npm release
  dependency set is exercised nowhere else except the release job itself, which runs on `main`
  after a Renovate bump has already merged.
* [`test/rbacparity/rbac_parity_test.go`](../../test/rbacparity/rbac_parity_test.go) — the
  operator ships two install paths, `helm install` from
  [`deploy/helm/mosquitto-operator`](../../deploy/helm/mosquitto-operator) and
  `kustomize build config/default`. Only the kustomize one is generated:
  [`config/rbac/role.yaml`](../../config/rbac/role.yaml) comes from the kubebuilder markers via
  `make generate-all`, while
  [`deploy/helm/mosquitto-operator/templates/clusterrole.yaml`](../../deploy/helm/mosquitto-operator/templates/clusterrole.yaml)
  is written by hand and nothing regenerates it. Both sides are manifests, so there is no
  compilation step for a divergence to break.

One of the three was measurably not doing its job, and it was green while not doing it.

`hack/verify-release-tooling.mjs` originally asserted only strings the `conventionalcommits`
preset produces: `Features`, `Bug Fixes`, and the three commit subjects from its synthetic
commit set. Those strings render whether or not
[`.github/release-template.hbs`](../../.github/release-template.hbs) is wired in, because the
template only replaces the preset's *main* template — the header and commit partials, the commit
transform and the group ordering all stay the preset's, as
[`hack/changelog-config.mjs`](../../hack/changelog-config.mjs) states. *(Amended 2026-10-10:
it replaces the preset's footer partial now, which leaves even more of the notes to the preset;
the argument holds unchanged.)* Disconnecting the
template from [`.releaserc.json`](../../.releaserc.json) therefore left every assertion
satisfied, and every future release note would have silently lost its Quality Gates section, its
`docker pull` line and its `helm repo add` line while CI stayed green.

Measured on 2026-09-01 and reproduced for this ADR: rendering the same commit set with the stock
preset and no `config` option satisfies all five original needles and does **not** contain
`Quality Gates`. The check has since been given three needles that only the template can
produce — `"Quality Gates"`, `"docker pull guidedtraffic/mosquitto-operator"` and
`"helm repo add mosquitto-operator"` — and with the template disconnected it now fails with
`rendered release notes are missing "Quality Gates" - the writer/preset pair renders incomplete
notes`, followed by the rendered notes so the reader can see what did come out. *(Amended
2026-10-10: four, the `docker pull` needle and a new `--version` one carrying the release
version; see Status.)*

The RBAC parity test was proven the other way round, by breaking the thing it guards rather than
the wiring: injecting a `delete` verb on `statefulsets` into the chart alone produces
`ClusterRole apps/statefulsets: the two install paths grant different verbs` with both verb sets
printed. And because a comparison of two empty sets is trivially equal, the test asserts it read
something at all: emptying the chart's RBAC makes it fail with `parsed no RBAC rules from the
Helm output` instead of passing.

## Decision

**D1 — A guard is trusted only after it has been observed failing against a deliberately broken
input.** Until that observation exists, the guard is an untested claim and is described as one.
Writing the guard, reading it, and watching it pass are all compatible with it being incapable
of failing.

**D2 — The break is the failure the guard exists to catch, not any failure.** Deleting
`hack/verify-release-tooling.mjs` and watching the job go red proves nothing. The break that
mattered there was the *silent* one — the template quietly falling out of the render path — and
it is the one that was performed. Likewise for RBAC, the break is a verb present on one side
only, which is what a forgotten kubebuilder marker actually produces.

**D3 — Assertions are on strings only the guarded artefact can produce.** The needle list in
`hack/verify-release-tooling.mjs` is split into two groups in the source, with the reason
written between them: the five preset-owned needles "still render when
`.github/release-template.hbs` is disconnected from `.releaserc.json`", and the ~~three~~
template-owned ones — four since 2026-10-10, two of them carrying the release version so an
unfilled placeholder fails too — are "what proves it is wired in". A needle that both the broken and the
working configuration produce is decoration.

**D4 — Every guard asserts that it read something.** `TestRBACParity_BothInstallPathsGrantTheSameAuthority`
calls `require.NotEmpty` on both decoded sides before comparing them, with the reason in place:
"A decoder that silently reads nothing would make this test pass forever." The same rule is why
`hack/verify-ci-references.mjs` treats zero selected files and zero total matches for a
`matchString` as failures rather than as an empty success.

**D5 — The observation is recorded with the exact message the guard emitted.** The three
messages above are quoted verbatim so a later reader can re-break the input and check that the
guard still fails the same way. A message that has silently changed is a guard that has silently
moved.

**D6 — Where a guard deliberately tolerates something, the tolerance is stated rather than left
to be discovered.** `hack/verify-ci-references.mjs` requires each `matchString` to hit somewhere
across the files a manager selects, but explicitly permits an individual selected file to
contribute zero hits, because `/^\.github/workflows/.*\.ya?ml$/` legitimately also selects
[`.github/workflows/renovate.yml`](../../.github/workflows/renovate.yml), which carries no
`GO_VERSION`. The per-file counts are printed either way, so the zero is visible in the log —
today's run prints `.github/workflows/renovate.yml (0)`.

**D7 — Guards run without a cluster, which is what lets them run on the pull request.** All
three targets say so in their `make help` text: `test-release-tooling` needs "node+npm, no
cluster", `verify-ci-references` needs "node only, no npm install", `verify-rbac-parity` needs
"helm + kustomize, no cluster". A guard that needs a Kind cluster lands in the E2E tier and is
paid for at E2E prices.

**D8 — The guard is entered through the Makefile, so the local and CI invocation are the same
target.** `generated-manifests` runs `make verify-rbac-parity`; `release-tooling` runs
`make verify-ci-references`. The single exception is deliberate and documented by its
surroundings: `release-tooling` runs `node hack/verify-release-tooling.mjs` directly, because the
job does its own `npm ci --ignore-scripts` step (and an `npm audit signatures` step) that
`make test-release-tooling` does not.

## Consequences

* **Proving a guard means breaking the repository on purpose.** That work does not survive in
  the tree — the broken input is thrown away — so the *recipe* has to survive somewhere, and
  this ADR is where it lives. A guard whose break is not written down will be re-derived by the
  next person or, more likely, not re-derived at all.
* **The template-owned needles are a maintenance cost with teeth.** Renaming the
  `## 📊 Quality Gates` heading in `.github/release-template.hbs`, or changing the Docker Hub
  image name in the `docker pull` line, turns `release-tooling` red for a perfectly legitimate
  edit. That is the intended trade: the check is coupled to the template's content precisely
  because that coupling is what detects the template going missing.
* **The parity test is not in the plain unit tier.** It sits behind the `rbacparity` build tag
  because it shells out to `helm` and `kustomize`, "which the plain unit tier must not require".
  A contributor without those binaries never runs it, and `verify-rbac-parity` depends on the
  `kustomize` Make target to install the pinned version rather than assuming one is present.
* **None of these guards is reachable from an aggregate target.** `all` is `build` and `test` is
  `fmt vet envtest`; the three guards are reached by naming them or by CI. Someone who runs
  `make test` before pushing has run none of them.
* **The local and CI installs of the release tooling are not the same command.**
  `make test-release-tooling` runs `npm ci --no-audit --no-fund`; the CI job runs
  `npm ci --ignore-scripts`. A dependency with an install hook therefore executes locally and
  not on the runner — which is the safer direction, but it does mean a green local run and a
  green CI run did not install identically.
* **These jobs execute fork-authored code on `self-hosted` runners.** The repository is public
  and `release.yml` triggers on `pull_request`; the header of that file records the accepted
  risk in full, including that repository secrets are not exposed to a fork run (GitHub passes
  only a read-only `GITHUB_TOKEN`), so the exposure is code execution on the runner rather than
  disclosure of `DOCKERHUB_PAT` or `BOT_PAT`, and that it is gated outside the repository under
  *Settings > Actions > General > "Fork pull request workflows from outside collaborators"*, and
  [ADR 0005](0005-fork-pull-requests-execute-on-the-self-hosted-runners.md) is where that
  decision lives. Adding a guard adds a step that a fork can cause to run.

## Alternatives Considered

### Trust a green check

This is the state that failed. `hack/verify-release-tooling.mjs` was green — green locally, and
wired into a job that runs on every pull request, though no run of that job on GitHub has been
observed — and it would have stayed green through the exact break it existed to prevent. A green
tick is evidence about the guard only after somebody has shown the guard can go red.

### Assert only that the tool did not throw

Rejected in the source itself: "A render that silently drops sections is as broken as one that
throws." The 2026-08-22 incident this script reproduces — a `conventional-changelog-writer`
version mismatch producing a handlebars "Missing helper" error — *did* throw, which is the easy
case. The template disconnection does not throw at all.

### Compare the two RBAC paths as text

Rejected: the two renderings differ legitimately in rule order, in whether `apiGroups` are
grouped or split, in verb order, and in the resource-name prefixes each path generates. The test
decodes both into `rbacv1` types and compares the set of `(kind, apiGroup, resource) -> verbs`,
so all of that washes out. Keeping the `kind` in the key is deliberate — "the same verbs on a
namespaced `Role` and on a `ClusterRole` are not the same authority".

### Generate the chart's `ClusterRole` from the kubebuilder markers too

Would remove the divergence instead of detecting it. Not done here: the chart's role is
hand-written and `make generate-all` does not touch it. What the tree records is the fact, not a
deliberation, so this is stated as an option that remains open rather than as one that was
weighed and rejected. The parity test is the compensating control in the meantime, and it names
the fix in its own failure message: "After changing a marker, mirror it into the chart."

### Run Renovate itself in CI to prove its `customManagers`

Rejected for the pull-request path — it needs a token and network access, and it would run for
fork-authored pull requests on self-hosted runners. The offline regex run is the substitute; see
[ADR 0003](0003-the-go-version-is-one-fact-in-four-files.md) for what it does and does not cover.

### Validate `renovate.json` against its JSON schema and stop there

`renovate.json` declares `"$schema": "https://docs.renovatebot.com/renovate-schema.json"`, and a
schema check would catch a malformed manager. It cannot catch a well-formed manager whose regex
matches no file, which is the entire failure class. Rejected as insufficient on its own.

### Prove the guards by unit-testing the guard scripts

Not taken. It would mean fixtures standing in for the real repository, and the failure that
mattered was precisely one where the fixture and the repository disagreed — the regexes were all
correct, the file set was not. Running the guard against the actual tree, and against the actual
tree deliberately broken, tests the thing that ships.

## Residual risks

* **One synthetic break per guard, not a catalogue.** Each of the three guards has been seen
  failing on purpose once or twice — `hack/verify-ci-references.mjs` on 2026-10-05, on a
  `matchString` that stopped matching its file — and `hack/verify-ci-references.mjs` also caught a
  real defect during the port (a missing `customManager` for `.github/workflows/*.yml`,
  [ADR 0003](0003-the-go-version-is-one-fact-in-four-files.md)). Which other synthetic breaks
  each one catches is inferred from reading it, not observed.
* **`verify-ci-references` tolerates zero hits per file within a manager, by design (D6), so it
  cannot detect a workflow file that has quietly lost its `GO_VERSION` line** as long as one
  other selected file still has one. Today `.github/workflows/renovate.yml (0)` is the
  legitimate zero; a second, illegitimate zero would look exactly like it. The tolerance is the
  price of a directory-wide `managerFilePatterns`, and it is accepted rather than worked around.
* **Nothing checks that a `versioningTemplate` accepts the value its regex captured.** That is
  the second failure recorded in [ADR 0003](0003-the-go-version-is-one-fact-in-four-files.md),
  and it is guarded by a comment in `renovate.json`, not by a check. Open.
* **`verify-ci-references` evaluates patterns with Node's regex engine, not RE2** — the script
  says so itself. A pattern Renovate cannot compile can still pass this check.
* **Every other check in the pipeline is unproven by this standard in this record.** `linter`,
  `gosec`, `govulncheck`, `cyclomatic-complexity`, the `generated-manifests` regenerate-and-diff
  guard, `mosquitto-image-tools`, the malware scans and the coverage gate have not been observed
  failing against a deliberately broken input in anything I can verify from this tree. That is
  not a claim that they are broken; it is the honest state of the evidence.
* **The observations were made locally, not on the runners.** macOS on `darwin/arm64`, with the
  two RBAC reproductions run against a `git archive` copy of `HEAD` and the two node checks run
  in the working tree. CI installs `helm v4.2.4` via `azure/setup-helm@v5` and runs on
  self-hosted Linux; the reproduction used `helm v3.21.3`. The helm major differs, and helm
  renders one half of the parity comparison.
* No workflow run of this repository has been inspected, and nothing here has run against a real
  Kubernetes cluster. Every statement above about what CI does describes the workflow files.

## References

* [`hack/verify-release-tooling.mjs`](../../hack/verify-release-tooling.mjs) — the two needle
  groups and why they are split; the 2026-08-22 incident the script reproduces
* [`hack/changelog-config.mjs`](../../hack/changelog-config.mjs) — how
  `.github/release-template.hbs` reaches the writer, and why it is a module rather than a JSON
  field
* [`.releaserc.json`](../../.releaserc.json) — the plugin configuration both the release job and
  the check read
* [`.github/release-template.hbs`](../../.github/release-template.hbs) — the footer the four
  template-owned needles come from
* [`test/rbacparity/rbac_parity_test.go`](../../test/rbacparity/rbac_parity_test.go) — the
  `grant` key, `authority`, the `require.NotEmpty` guard, and the hint printed on failure
* [`config/rbac/role.yaml`](../../config/rbac/role.yaml) — the generated half
* [`deploy/helm/mosquitto-operator/templates/clusterrole.yaml`](../../deploy/helm/mosquitto-operator/templates/clusterrole.yaml)
  — the hand-written half
* [`hack/verify-ci-references.mjs`](../../hack/verify-ci-references.mjs) — the third guard, and
  its own account of what it tolerates and cannot cover
* [`Makefile`](../../Makefile) — `test-release-tooling`, `verify-ci-references`,
  `verify-rbac-parity`
* [`.github/workflows/release.yml`](../../.github/workflows/release.yml) — the
  `generated-manifests` and `release-tooling` jobs, and the fork-pull-request risk recorded at
  the top of the file
* [ADR 0003](0003-the-go-version-is-one-fact-in-four-files.md) — the two Renovate failures that
  produced `verify-ci-references`, and the one it still does not cover
* [ADR 0006](0006-both-install-paths-grant-the-same-authority.md) — the RBAC parity decision this
  ADR only supplies the evidence for
* [ADR 0005](0005-fork-pull-requests-execute-on-the-self-hosted-runners.md) — who else can cause
  these guards to run
