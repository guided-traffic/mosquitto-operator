# ADR 0004: Two E2E legs, shaped by node count, and no version matrix

## Status

Accepted. Date: 2026-09-01.

**Verified by reading**
[`.github/workflows/release.yml`](../../.github/workflows/release.yml) (the `on:` block, the
`e2e-tests` job with its `strategy.matrix.include`, the Kind config heredoc, the
`Verify Kind cluster` node-count step, the `Run E2E tests` step and its guard, the `e2e-gate`
job, and the `needs:` list of `semantic-release`),
[`test/e2e/affinity_test.go`](../../test/e2e/affinity_test.go) (in full),
[`test/e2e/e2e_test.go`](../../test/e2e/e2e_test.go) (package doc, constants, client setup, and
a grep for every `E2E_` name and `os.Getenv` call in it),
[`test/e2e/helm-values.yaml`](../../test/e2e/helm-values.yaml),
[`test/testimages/images.go`](../../test/testimages/images.go),
[`renovate.json`](../../renovate.json) (the `eclipse-mosquitto` packageRule) and the E2E
section of the [`Makefile`](../../Makefile) (`E2E_RUN`, `E2E_MOSQUITTO_IMAGE`, `test-e2e`,
`KIND_CLUSTER`, `KIND_WORKERS`, `kind-create`, `kind-delete`, `kind-load`,
`cert-manager-install`, `E2E_IMG`, `e2e-local`).

**Verified by running** two things that cost nothing and settle a claim this ADR makes about
reproducing a leg locally: `make -n test-e2e E2E_RUN=TestE2E_AntiAffinity` renders
`-run 'TestE2E_AntiAffinity'` and `make -n test-e2e` renders no `-run` flag at all; and a
throwaway GNU make probe confirmed that a variable given on the command line reaches a sub-make
while an environment variable reaches the recipe shell — which is what makes
`E2E_REQUIRE_MULTI_NODE=true make e2e-local KIND_WORKERS=3 …` work through `e2e-local`, whose
recipe runs `$(MAKE) test-e2e` and then tears the cluster down with `$(MAKE) kind-delete`.

**Not verified.** Nothing in this repository has ever run against a real cluster, in CI or
locally: there is no workflow run, no Kind cluster and no recorded E2E result behind any
statement here. Every claim below is a claim about what the files say, never about what a run
did. In particular, three things are recorded rationale rather than measurement: that kind keeps
the `NoSchedule` taint on the control-plane node of a multi-node cluster and drops it on a
single-node one; that `grep -q -- "--- PASS: …"` matches the `go test -v` output the suite will
actually emit for a top-level parallel test; and that GitHub counts a skipped required check as
satisfied, which is the reason `e2e-gate` carries `if: always()`.

Implemented: the two legs, the gate job, the node-count assertion, the guard grep, the
`E2E_REQUIRE_MULTI_NODE` escalation and the `KIND_WORKERS` variable all exist in the tree today.
Open: the local reproduction is close but not identical to a leg — see Residual risks.

## Context

The E2E suite is five tests
([`test/e2e/`](../../test/e2e/)): `TestE2E_Mosquitto_ProvisionsAReachableBroker`,
`TestE2E_AntiAffinity_OffByDefault`, `TestE2E_AntiAffinity_SoftWhenRequested`,
`TestE2E_AntiAffinity_HardSpreadsAcrossNodes` and
`TestE2E_TLS_CertManagerIssuedSecretServesMQTTS`. Exactly one of them has an outcome that
depends on the shape of the cluster underneath it: hard anti-affinity is a scheduling
constraint, and a constraint that cannot be violated on a one-node cluster is a constraint
nothing tested.

That single test is the whole reason a matrix exists. Everything else the operator does —
render a StatefulSet, a headless Service, a client Service, a ConfigMap, mount a TLS Secret —
produces the same objects on one node as on four.

Two forces pull against each other:

* **A second cluster shape costs a full leg.** Each leg creates its own Kind cluster, installs
  cert-manager, builds and imports the operator image into every node, preloads the pinned
  broker image and installs the chart. That is minutes of runner time, and the job carries
  `timeout-minutes: 60`.
* **A leg that silently degrades is worse than no leg.** The multi-node leg exists to assert a
  node count. If the cluster comes up smaller, or if the test that asserts it stops being
  selected, the leg still reports green — and green from a leg that executed nothing is the
  failure mode that motivates most of the machinery below.

There is also a dimension this repository deliberately does not have. The obvious second axis
for an operator that provisions an upstream image is the version line of that image. Here that
image is a single pin, `MosquittoImage = "eclipse-mosquitto:2.1.2-alpine"` in
[`test/testimages/images.go`](../../test/testimages/images.go), held on the 2.x line by a
`renovate.json` packageRule with `"allowedVersions": "<3"` whose description says a move to a
future major is a decision rather than a dependency update
([ADR 0007](0007-one-broker-image-pin-and-why-not-the-openssl-tag.md) owns that pin). A
version axis over a set with one member is not a matrix; it is a column that has to be read,
maintained and explained without ever changing an outcome.

## Decision

**D1 — The E2E matrix has exactly two legs, and the axis is the node count.**
`strategy.matrix.include` in the `e2e-tests` job carries two entries and nothing else:

| `topology` | `workers` | `cluster` | `run_filter` | `require_multi_node` |
|---|---|---|---|---|
| `single-node` | `0` | `mosquitto-operator-test` | `""` | `"false"` |
| `multi-node` | `3` | `mosquitto-operator-test-multinode` | `TestE2E_AntiAffinity` | `"true"` |

The two values reach the steps as job-level `env:` — `KIND_CLUSTER: ${{ matrix.cluster }}` and
`KIND_WORKERS: ${{ matrix.workers }}` — so the same steps build a different cluster per leg
rather than the legs branching on `if:`. `fail-fast: false`, because a failure on one shape is
information about the other, not a reason to cancel it. The distinct cluster names are what
keeps the two legs collision-free when they run in parallel.

**D2 — There is no version-line dimension, and the escape hatch is a variable rather than an
axis.** One broker pin, one line. When somebody needs to run the suite against another image,
`E2E_MOSQUITTO_IMAGE` names it: the `test-e2e` recipe passes it through to the test binary and
`testimages.Default()` prefers it over `MosquittoImage` when it is non-empty. That covers the
one-off question a version axis would answer, at the cost of nothing when nobody asks it.

**D3 — The multi-node leg gets three workers, not two.** The scenario is `replicas: 3` with
`antiAffinity: hard`, which needs three distinct nodes a broker pod can actually land on. On a
multi-node kind cluster the control-plane node stays `NoSchedule`-tainted, so it is not one of
them — `schedulableNodeCount` counts only nodes that are `Ready`, not `Unschedulable` and free
of a `NoSchedule` or `NoExecute` taint, and `hasNoScheduleTaint` exists precisely so the tainted
control-plane cannot inflate the count past the threshold. Two workers would leave the third pod
`Pending`, which is a red leg that looks like an operator defect and is not one.

**D4 — A leg refuses to test on a cluster that is not the shape it asked for.** The
`Verify Kind cluster` step computes `expected=$(( KIND_WORKERS + 1 ))`, compares it against
`kubectl get nodes --no-headers | wc -l`, and exits 1 on a mismatch, naming the leg in the error.
This is a different measure from D3 on purpose: the workflow counts every node the cluster has,
because it is checking that the cluster came up as requested, while the test counts the nodes a
pod could be scheduled onto, because it is checking that the assertion is satisfiable. On the
multi-node leg the two land on 4 and 3 respectively, and both have to hold.

**D5 — A leg that runs with a `-run` filter proves in its own log that the scenario it exists
for executed.** The `Run E2E tests` step pipes through `tee` into
`tmp/e2e-${{ matrix.topology }}.log` and then, only when `E2E_RUN` is non-empty, requires
`grep -q -- "--- PASS: TestE2E_AntiAffinity_HardSpreadsAcrossNodes"` in that log. The guard
exists because a `-run` filter is a regex against test names and nothing keeps it in step with
the names: a rename, a move or a split leaves `go test` matching nothing, exiting 0, and the leg
green having executed no assertion at all. The test carries a doc comment saying its name is
greppable on purpose, so the coupling is visible from both ends. The single-node leg has an
empty filter and therefore no guard — nothing there is selected by name.

**D6 — On the leg whose purpose is the node count, "not enough nodes" is a failure, not a
skip.** `E2E_REQUIRE_MULTI_NODE: ${{ matrix.require_multi_node }}` is `"true"` on the multi-node
leg only. `requireThreeSchedulableNodes` returns quietly at three or more schedulable nodes;
below that it calls `t.Fatalf` when `os.Getenv("E2E_REQUIRE_MULTI_NODE") == "true"` and
`t.Skipf` otherwise. A skip here would be worse than useless: it is not a neutral outcome but a
green one, reported by the only leg that was ever going to catch a broken hard spread, and it
would arrive exactly when the cluster failed to come up the way the leg needed — the moment the
signal matters most. The skip is kept for the single-node leg and for any developer cluster,
where the same test genuinely has nothing to say. D5 and D6 overlap deliberately, and the
overlap is worth its cost: the guard grep would also catch a skip, because `--- SKIP:` is not
`--- PASS:`, but it would report it as "the filter no longer matches", which sends the reader
after the wrong defect. D6 makes the harness say what actually went wrong.

**D7 — Branch protection points at `e2e-gate`, never at a matrix leg.** A matrix job reports one
status context per leg, named `E2E Tests (single-node)` and `E2E Tests (multi-node)` from
`name: E2E Tests (${{ matrix.topology }})`, so every reshaping of the matrix renames or removes
a required check and quietly stops enforcing it. The `e2e-gate` job carries the stable name
`E2E Tests`, `needs: [e2e-tests]`, `if: always()`, and one step that reads
`needs.e2e-tests.result` and asserts `[ "${result}" = "success" ]`. `if: always()` is load
bearing: without it the gate would be skipped whenever the matrix failed or was cancelled, and a
skipped required check does not block a merge. The release path is wired independently —
`semantic-release.needs` lists `e2e-tests` itself, not the gate — so a broken gate cannot ship a
release and a broken matrix cannot either.

**D8 — Every leg has a stated local invocation, and the worker count is a variable so the
statement can be true.** `KIND_WORKERS ?= 3` in the [`Makefile`](../../Makefile), consumed by
`kind-create`, which emits one `- role: worker` line per worker next to the single
`- role: control-plane`. Today:

```bash
# single-node leg
make e2e-local KIND_WORKERS=0

# multi-node leg
E2E_REQUIRE_MULTI_NODE=true make e2e-local KIND_WORKERS=3 \
  KIND_CLUSTER=mosquitto-operator-test-multinode \
  E2E_RUN=TestE2E_AntiAffinity
```

Both forms work through `e2e-local`, whose recipe ends in `$(MAKE) test-e2e`: command-line
variables reach the sub-make, and `E2E_REQUIRE_MULTI_NODE` reaches the test binary as an
ordinary environment variable. The default is 3 rather than 0 because the anti-affinity scenario
is the one a developer most often wants a real cluster for.

**D9 — The worker lines come from a counting loop, in both places.** Neither `kind-create` nor
the workflow heredoc uses `seq`: BSD `seq` on macOS counts *down* when the first bound exceeds
the second, so `seq 1 0` prints `1 0` and `KIND_WORKERS=0` would build a two-worker cluster on a
developer machine — a single-node reproduction that is not single-node, which is the exact class
of false claim D8 exists to remove.

## Consequences

* **The suite is verified on one node and on four, and on nothing else.** No leg covers two
  nodes, a cluster with a mix of tainted and untainted workers, or more than four nodes. That is
  a deliberate hole, not an oversight: nothing in the operator branches on those shapes today.
* **The multi-node leg re-runs more than it needs.** `TestE2E_AntiAffinity` is an unanchored
  regex, so it selects `OffByDefault` and `SoftWhenRequested` as well as
  `HardSpreadsAcrossNodes` — three of the suite's five tests, two of which already ran on the
  single-node leg. Kept as-is: an anchored filter naming one test would have to be edited every
  time a node-shape-sensitive test is added, and the two extra tests are cheap next to the
  cluster they run on.
* **`TestE2E_Mosquitto_ProvisionsAReachableBroker` and
  `TestE2E_TLS_CertManagerIssuedSecretServesMQTTS` run on the single-node leg only.** A defect
  that only appears when the operator and the broker sit on different nodes would not be caught.
* **The hard-spread test is skipped on the single-node leg**, where
  `schedulableNodeCount` returns 1 and `require_multi_node` is `"false"`. So the assertion that
  matters most runs on exactly one leg, and every guard in D4, D5 and D6 exists to protect that
  one execution.
* **`e2e-gate` burns a `self-hosted` runner to compare two strings.** It has to: a status context
  only exists if a job produces one. The cost is one short runner slot per workflow run.
* **The guard grep couples CI to the exact test name.** Renaming
  `TestE2E_AntiAffinity_HardSpreadsAcrossNodes` without editing
  [`.github/workflows/release.yml`](../../.github/workflows/release.yml) fails the multi-node
  leg. That is the intended direction of the coupling — loud, and at the right moment — but it
  is a coupling, and it is not enforced by anything at compile time.
* **The local reproduction is close, not identical.** `make e2e-local` creates its Kind cluster
  from a plain `kind create cluster --config tmp/kind-config.yaml` heredoc that carries only the
  name and the node list, while the workflow writes a `kind-config.yaml` that also sets
  `networking.kubeProxyMode: "iptables"` and a `containerdConfigPatches` block selecting the
  `native` snapshotter for DinD, and drives it through `helm/kind-action@v1.14.0`. Locally there
  is no host preparation, no per-node `ctr images import`, no broker-image preload and no guard
  grep. A developer reproducing a leg reproduces the *topology* and the *test selection*, not the
  runner environment.

## Alternatives Considered

### One leg, on a multi-node cluster only

Rejected. The whole suite would then depend on a cluster shape most developers do not have
locally, and `TestE2E_AntiAffinity_OffByDefault` would stop covering the case it exists for: the
default emits no affinity block, which is only interesting where scheduling could have been
constrained.

### Two workers on the multi-node leg

Rejected — see D3. With the control-plane tainted, two workers cannot satisfy a hard spread of
three replicas, so the leg would fail for a reason that has nothing to do with the operator.

### A version-line axis over Mosquitto releases

Rejected. There is one pin, held on the 2.x line by a `renovate.json` rule; a second value would
have to be invented to fill the column. `E2E_MOSQUITTO_IMAGE` covers the occasional one-off
without a permanent axis (D2).

### Make the matrix legs the required checks directly

Rejected — see D7. `E2E Tests (single-node)` and `E2E Tests (multi-node)` are matrix-derived
names; adding, removing or renaming a leg silently changes which contexts exist, and branch
protection keeps requiring a context nobody produces any more or stops requiring one nobody
registered.

### Let the hard-spread test skip everywhere

Rejected — see D6. A skip is reported as a pass, and the leg that skips is the only one that
would have caught the defect.

### Trust `go test`'s exit code instead of grepping the log

Rejected — see D5. A `-run` filter that matches nothing exits 0. The exit code answers "did
anything fail", never "did anything run".

### Assert the node count only inside the test

Rejected as too late and too narrow. The workflow's `Verify Kind cluster` step fails within
seconds of cluster creation, before cert-manager, the image builds and the chart install have
each spent their minutes, and it also protects the legs that do not touch
`requireThreeSchedulableNodes` at all.

### Leave `kind-create` hard-coded at three workers

This was the previous state, and it is the defect this ADR closes. `kind-create` always built
three workers, so the comment claiming `make e2e-local` reproduced the single-node leg was
simply false — the invocation produced a four-node cluster on which the single-node leg's
distinguishing property did not hold. `KIND_WORKERS` exists so that D8 can state a true
invocation for each leg.

## Residual risks

* **Nothing here has been observed running.** The legs, the gate, the node-count assertion and
  the guard grep have never executed against a cluster or on a runner. Every claim in this ADR
  is about the content of files. The first real run is where the shape of `go test -v` output,
  the kind taint behaviour and the DinD preparation stop being reasoning and start being facts.
* **The guard grep depends on the exact output format of `go test -v`.** It matches
  `--- PASS: TestE2E_AntiAffinity_HardSpreadsAcrossNodes` in a log that also contains `=== RUN`,
  `=== PAUSE` and `=== CONT` lines, because the test calls `t.Parallel()`. Top-level results are
  expected to be unindented and subtests indented, which is why the pattern carries no leading
  whitespace. Not verified against real output.
* **The control-plane taint claim is recorded, not measured.** D3 and the comments in
  [`test/e2e/affinity_test.go`](../../test/e2e/affinity_test.go) and the
  [`Makefile`](../../Makefile) all rest on kind keeping the `NoSchedule` taint on the
  control-plane node of a multi-node cluster and dropping it on a single-node one. The code
  defends against being wrong — `schedulableNodeCount` computes the count instead of assuming it
  — but the worker count 3 was chosen from that assumption.
* **`E2E_REQUIRE_MULTI_NODE` is honoured in exactly one place.** It is read only by
  `requireThreeSchedulableNodes` in [`test/e2e/affinity_test.go`](../../test/e2e/affinity_test.go);
  [`test/e2e/e2e_test.go`](../../test/e2e/e2e_test.go) has no `TestMain` and reads no `E2E_`
  variable at all, and the `test-e2e` recipe does not set it. A future node-shape-sensitive test
  that forgets to call the helper inherits none of D6, and nothing detects that.
* **A skip anywhere else in the suite is still silent.** D5 and D6 protect one named test. Any
  other test that starts skipping — for a missing ClusterIssuer, a missing storage class — is
  reported as a pass by both legs.
* **`make e2e-local` assumes `E2E_IMG` and the chart values agree.** The recipe builds and loads
  `E2E_IMG ?= mosquitto-operator:test` and then installs the chart with
  [`test/e2e/helm-values.yaml`](../../test/e2e/helm-values.yaml), which hard-codes
  `image.repository: mosquitto-operator`, `image.tag: test` and `pullPolicy: Never`. Overriding
  `E2E_IMG` alone produces an install pointing at an image that is not on the node, and with
  `pullPolicy: Never` the kubelet then refuses to start the pod rather than reporting anything
  that names the mismatch. (What the kubelet reports in that case is Kubernetes behaviour and
  was not verified here.) The
  workflow does not have this hazard, because it passes `--set image.repository` and
  `--set image.tag` explicitly.
* **Coverage of the node-count dimension stops at hard anti-affinity.** If the operator later
  gains behaviour that depends on topology in another way — spread constraints, zone awareness —
  the multi-node leg will not cover it until somebody extends the filter, and D5's guard will
  keep passing while it does not.

## References

* [`.github/workflows/release.yml`](../../.github/workflows/release.yml) — the `e2e-tests`
  matrix, the Kind config heredoc, the `Verify Kind cluster` node-count step, the `Run E2E tests`
  guard, the `e2e-gate` job, and `semantic-release.needs`
* [`test/e2e/affinity_test.go`](../../test/e2e/affinity_test.go) —
  `TestE2E_AntiAffinity_HardSpreadsAcrossNodes`, `multiNodeRequiredEnv`,
  `requireThreeSchedulableNodes`, `schedulableNodeCount`, `hasNoScheduleTaint`, `isNodeReady`
* [`test/e2e/e2e_test.go`](../../test/e2e/e2e_test.go) — the suite's clients and the API surface
  it addresses as literals
* [`test/e2e/helm-values.yaml`](../../test/e2e/helm-values.yaml) — `pullPolicy: Never` and the
  image coordinates the local run depends on
* [`Makefile`](../../Makefile) — `E2E_RUN`, `E2E_MOSQUITTO_IMAGE`, `test-e2e`, `KIND_CLUSTER`,
  `KIND_WORKERS`, `kind-create`, `cert-manager-install`, `E2E_IMG`, `e2e-local`
* [`test/testimages/images.go`](../../test/testimages/images.go) — `MosquittoImage`,
  `EnvMosquittoImage`, `Default()`
* [`renovate.json`](../../renovate.json) — the `eclipse-mosquitto` packageRule with
  `"allowedVersions": "<3"`
* [ADR 0005](0005-fork-pull-requests-execute-on-the-self-hosted-runners.md) — why every one of
  these legs runs on `self-hosted`, and what a fork pull request does to them
* [ADR 0007](0007-one-broker-image-pin-and-why-not-the-openssl-tag.md) — the single broker image
  pin that D2 declines to turn into a matrix axis
