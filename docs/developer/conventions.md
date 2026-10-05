# Conventions

What every change in this repository follows. Read against the tree on 2026-10-05.

- **English everywhere** — code, comments, commit messages, documentation, CRD field docs.
- **Conventional Commits.** `semantic-release` derives the version from them
  (`@semantic-release/commit-analyzer` with the `conventionalcommits` preset): `fix` is a patch,
  `feat` a minor, a `!` after the type or a `BREAKING CHANGE:` footer a major. Renovate writes `fix`
  for minor, patch, digest and pin updates and `chore` for every GitHub Actions update, so its
  PRs land in the right release-notes section ([ci-and-release.md](ci-and-release.md#the-release)).
- **The Makefile is the entry point** for tests, linting, analysis and generation, locally and in
  CI. Tools resolve to `bin/`, never to `PATH` ([build-test-lint.md](build-test-lint.md)).
- **Go:** `gofmt` and `goimports` with `github.com/guided-traffic/mosquitto-operator` as the local
  prefix, the linter set in [`.golangci.yml`](../../.golangci.yml) (errcheck, govet, ineffassign,
  staticcheck, unused, misspell — with `mosquitto` exempt, or it would rename the API — unconvert,
  unparam, goconst, prealloc, revive with its listed rules). A wrapped error carries `%w` behind a
  lowercase context (`setting owner reference on ConfigMap %s: %w`,
  `parsing spec.storage.size %q: %w`). Cyclomatic complexity under 15 per function; `make cyclo`
  is the gate.
- **Comments explain why, never what.** The Makefile and the workflows carry long comments about
  the DinD kernel modules, the inotify limits, the kube-proxy iptables race, the 131072-byte cap on
  a `with:` input, the gosec memory bound and the guard greps. Keep them accurate: a comment
  describing machinery this repository does not have is worse than no comment.
- **Generated files are never edited by hand.** `api/v1/zz_generated.deepcopy.go`,
  `config/crd/bases/`, `config/rbac/role.yaml` and
  `deploy/helm/mosquitto-operator/templates/crd.yaml` come from `make generate-all`, and CI fails
  when the committed copies differ. Run it after any change under `api/v1/` or to an RBAC marker.
- **A new RBAC marker updates the chart's `clusterrole.yaml` in the same change**, and
  `make verify-rbac-parity` proves it ([adding-things.md](adding-things.md#a-managed-object)).
- **Every write onto a generated name is preceded by `ensureOwned`**, and nothing deletes or
  patches ([ADR 0009](../adr/0009-delete-only-through-owner-references.md)).
- **Deterministic names come from one helper each**, label sets from `common.BaseLabels` and
  `common.SelectorLabels`. The StatefulSet and Service names live in `internal/common`, the
  ConfigMap name in `internal/builder`; a builder that invents a name inline is a name no other
  package can find.
- **The broker image pin lives in exactly two constants**, held equal by a test; it is copied
  nowhere else — not into the Makefile, not into a workflow
  ([ADR 0007](../adr/0007-one-broker-image-pin-and-why-not-the-openssl-tag.md)).
- **Tests:** testify (`require` for preconditions, `assert` for the claim); a test name states the
  behaviour (`TestReconcile_RefusesForeignObjects`, `TestE2E_<Area>_<Behaviour>`); a test goes into
  the cheapest tier that can answer its question and never into one that cannot — envtest starts
  no pod and collects no garbage ([testing.md](testing.md)).
- **One build tag per tier**, and each tier states its own blind spot in its package comment. A
  check that can run without a cluster belongs where it runs on the pull request.
- **No `-short`, no `testing.Short()`.** CI runs the coverage targets, so such a gate would remove
  the tests behind it from every automated run while they still pass locally.
- **A new guard is proven by breaking it** — with the failure it exists to catch, not any failure —
  and the message it emitted is recorded in
  [ADR 0010](../adr/0010-a-check-is-not-a-check-until-it-has-failed-on-purpose.md). Its assertions
  are on strings only the guarded artefact can produce, and it asserts that it read something.
- **New dependencies are justified.** `cert-manager` appears nowhere in `go.mod` or `go.sum`; the
  E2E suite talks to it through the dynamic client to keep it that way.
  `prometheus/client_golang` is in the graph only as an indirect dependency of controller-runtime's
  metrics server — nothing here registers a metric of its own.
- **Temporary files** go into the repository's `tmp/` (ignored), never into the system `/tmp`.
- **Decisions are ADRs** in [docs/adr/](../adr/README.md). A changed decision is an amendment in
  place — the new rule in `Decision`, the amendment with its date in `Status`, the old rule marked —
  in the same change as the code. A new ADR is added to the index in the same change.
- **Documentation:** every claim verified against the tree, and "not verified" is a complete
  sentence; nothing here has run against a real cluster, and pages say so where it matters; file
  references as relative links; `# default` / `# example` on shown values; whoever changes
  behaviour updates the page that describes it in the same change.
- **Security:** a change that weakens authentication, secrets, TLS, permissions, isolation,
  validation or exposure is named as such and discussed before it is implemented; an accepted risk
  is written down on the security page it belongs to ([docs/security/](../security/README.md)).
