---
id: T6
title: the release pipeline holds more authority than it uses and runs unreviewed code beside it
state: dropped
severity: medium
security: hardening
threat: would additionally cover a compromised third-party action or a moved image tag reaching the Docker Hub credential, the release token or the published image
urgency: later        # rule 4: decided or cheap known fixes
effort: S
blocked-by:
filed-from: the documentation restructuring of 2026-10-05 (security pages)
opened: 2026-10-05
decided:
done:
dropped-reason: folded into the project plan, which is the work list (ADR 0011 D12)
---

## Current state

No concrete attack path today; each item widens what a compromise elsewhere could reach. The public
gaps they belong to are H-9 and H-10 on
[docs/security/ci-and-supply-chain.md](../../security/ci-and-supply-chain.md).

- **Unused job permissions.** `semantic-release` declares `contents`, `issues`, `pull-requests` and
  `id-token` write ([`release.yml:1256-1260`](../../../.github/workflows/release.yml#L1256-L1260)); every
  write it makes goes through the GitHub App token, so none of the job token's scopes is used.
- **No `permissions:` floor in `build.yml`** — the only workflow without a top-level block
  (`release.yml:28` has one).
- **The Docker Hub credential stays on disk in `build`.** No `docker logout` runs in that job, so
  `anchore/sbom-action@v0` ([`build.yml:115`](../../../.github/workflows/build.yml#L115)) and
  `softprops/action-gh-release@v3` (`build.yml:125`, `:259`) run with it present.
- **Tag-referenced third-party actions outside the documented exception list:**
  `anchore/sbom-action@v0`, `softprops/action-gh-release@v3`,
  `marocchino/sticky-pull-request-comment@v3` (`release.yml:1089`),
  `renovatebot/github-action@v46.3.6` (`renovate.yml:58`). The comment at `release.yml:1172-1175`
  says only Trivy is such an exception. Removing `digest` from automerge does not stop a moved tag.
- **A frozen BuildKit.** `build.yml:45` pins `moby/buildkit:v0.12.0` with `network=host`, and no
  Renovate manager moves it.
- **Images by tag with unreviewed automerge.** The broker default image, both `Containerfile` base
  images and the chart's operator image are referenced by tag; Renovate automerges their minor,
  patch and digest updates ([`renovate.json:128-139`](../../../renovate.json#L128-L139), `:217-228`).

## Required changes

1. `semantic-release`: `permissions: contents: read` (or none beyond what checkout needs);
   `build.yml`: a top-level `permissions: contents: read`, widened per job.
2. `docker logout` right after the push step in `build`, before any third-party action runs.
3. Third-party actions pinned by commit SHA with a Renovate `digest` manager, or the exception list
   in the comment and on the security page extended honestly.
4. BuildKit: a Renovate `customManager` for the `image=` line (the guard of
   `make verify-ci-references` covers it), or the explicit pin dropped.
5. Image automerge: decide per image whether minor updates need review; at least the broker image,
   which the operator ships as a default into every cluster.
6. The security page's H-9 and H-10 updated in the same change.

## Not verified

- Whether `moby/buildkit:v0.12.0` is affected by the BuildKit advisories of early 2024
  (CVE-2024-23651, -23652, -23653); the recollection that they were fixed in v0.12.5 has not been
  checked against an advisory database.
