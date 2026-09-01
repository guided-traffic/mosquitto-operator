# ADR 0007: One broker image pin, on `2.1.2-alpine`, and not on the `-openssl` tag

## Status

Accepted. Date: 2026-09-01.

**Verified by reading, in this repository:**
[`internal/builder/statefulset.go`](../../internal/builder/statefulset.go) (`DefaultImage`,
`ResolveImage`, the broker `Command`),
[`test/testimages/images.go`](../../test/testimages/images.go) (`MosquittoImage`,
`EnvMosquittoImage`, `Default`),
[`test/testimages/images_test.go`](../../test/testimages/images_test.go)
(`TestMosquittoImageIsPinnedToATag` and the three `Default` cases),
[`test/imagetools/image_tools_test.go`](../../test/imagetools/image_tools_test.go)
(`TestPinnedImageIsTheOperatorDefault`, `TestImageProvidesEveryExecutedTool`, `clientTools`,
`brokerCommand`), [`renovate.json`](../../renovate.json) (the broker-image `customManagers` entry
and the `allowedVersions` packageRule),
[`hack/verify-ci-references.mjs`](../../hack/verify-ci-references.mjs),
[`Makefile`](../../Makefile) (`test-image-tools`, `verify-ci-references`),
[`.github/workflows/release.yml`](../../.github/workflows/release.yml) (the
`mosquitto-image-tools` job),
[`internal/builder/configmap.go`](../../internal/builder/configmap.go) (what the generator emits),
and the rendered CRD [`config/crd/bases/mko.gtrfc.com_mosquittoes.yaml`](../../config/crd/bases/mko.gtrfc.com_mosquittoes.yaml)
(the seven `spec` properties).

**Verified by running, on 2026-09-01:** `node hack/verify-ci-references.mjs`, which reports
`OK` for the broker-image customManager with one hit each in `internal/builder/statefulset.go` and
`test/testimages/images.go`; and `docker manifest inspect` plus `docker run` against the upstream
images (see Context for the numbers, and Residual risks for what the numbers do and do not mean).

**Not verified:** no broker has been observed running on a real cluster from this repository. The
image evidence below says what is *in* the image; it says nothing about how `mosquitto 2.1.2`
behaves under load, and the 2.1 deprecation and `max_packet_size` claims come from upstream
release notes, not from anything measured here.

## Context

The broker image appears twice in this repository, for two different reasons:

* `builder.DefaultImage` — the image a `Mosquitto` resource gets when `spec.image` is empty. This
  one reaches every user's cluster.
* `testimages.MosquittoImage` — the image the E2E suite provisions. This one reaches CI.

Two copies of a version string is a defect waiting to happen: if they drift, the image-tools job
verifies an image no broker runs, and the E2E suite proves a listener works on a tag nobody
deploys. So the first question is how to keep two constants equal. The second is which tag they
should hold, and there the upstream tag list is actively misleading — `eclipse-mosquitto` publishes
`2.0.22` *and* `2.0.22-openssl`, which reads like a choice between a build with TLS and a build
without one.

**It is not a choice, and upstream says so directly.** Every image in the Docker Hub official
library carries an `org.opencontainers.image.source` annotation naming the directory it was built
from. Read on 2026-09-01 with `docker buildx imagetools inspect`:

| Tag | Index digest (`Manifest.Digest`) | Built from |
|---|---|---|
| `2.0.22` | `sha256:212f89e1eaeb2c322d6441b64396e3346026674db8fa9c27beac293405c32b3c` | `docker/2.0-openssl` |
| `2.0.22-openssl` | `sha256:212f89e1eaeb2c322d6441b64396e3346026674db8fa9c27beac293405c32b3c` | `docker/2.0-openssl` |
| `openssl` | `sha256:212f89e1eaeb2c322d6441b64396e3346026674db8fa9c27beac293405c32b3c` | `docker/2.0-openssl` |
| `2.1.2-alpine` | `sha256:6f8d8a947c506f8a2290ec65cd4bd2bc7cb4d43fb5f6271f861cb013e2ef9797` | `docker/2.1-alpine` |
| `latest` | `sha256:6f8d8a947c506f8a2290ec65cd4bd2bc7cb4d43fb5f6271f861cb013e2ef9797` | `docker/2.1-alpine` |
| `2` | `sha256:6f8d8a947c506f8a2290ec65cd4bd2bc7cb4d43fb5f6271f861cb013e2ef9797` | `docker/2.1-alpine` |

Two facts, and the second is the stronger one:

1. `2.0.22` and `2.0.22-openssl` resolve to the **same index digest** — one artifact, two names.
2. `2.0.22` is **built from `docker/2.0-openssl`**. The plain `docker/2.0` directory is not what
   produces the default 2.0 tag any more. The identical digest is not a coincidence to be
   re-measured every quarter; it is the same build, and upstream's own annotation records that.

The 2.1 line settles it further: it ships `-alpine` and `-ubuntu` variants and no openssl split at
all, because there is nothing left to split.

**Which digest is quoted matters.** The values above are *index* digests — what
`docker buildx imagetools inspect <tag> --format '{{.Manifest.Digest}}'` prints, and what
identifies the tag. Inside each index sit one image manifest per platform with their own digests
(`sha256:54c90ecc7864…` is the `linux/amd64` manifest inside `2.0.22`), and a pulled image has a
third hash again, its local image ID. All three are real and none equals another. A reader
checking this table must compare index digests with index digests.

And measured inside the images, on `arm64/linux`, with `docker run --entrypoint sh`:

* `ldd /usr/sbin/mosquitto` resolves `libssl.so.3 => /usr/lib/libssl.so.3` and
  `libcrypto.so.3 => /usr/lib/libcrypto.so.3` in **both** `2.0.22` and `2.1.2-alpine`. That is
  OpenSSL 3 in the default image, dynamically linked by the broker binary itself.
* Both images provide `mosquitto` (`/usr/sbin/mosquitto`), `mosquitto_passwd`, `mosquitto_sub`,
  `mosquitto_pub`, `mosquitto_ctrl` (all in `/usr/bin`) and `sh` (`/bin/sh`).
* `mosquitto -h` reports `mosquitto version 2.0.22` and `mosquitto version 2.1.2` respectively.

So the `-openssl` variant is a second name for the artifact the plain tag already resolves to, and
TLS support was never the thing that distinguished them. What remains to decide is 2.0 versus 2.1.

## Decision

**D1 — The pinned broker image is `eclipse-mosquitto:2.1.2-alpine`.**
It is written in exactly two places, both with a `// renovate: datasource=docker
depName=eclipse-mosquitto` comment directly above:
`builder.DefaultImage` in
[`internal/builder/statefulset.go`](../../internal/builder/statefulset.go) and
`testimages.MosquittoImage` in [`test/testimages/images.go`](../../test/testimages/images.go).

**D2 — No `-openssl` tag is used anywhere.** It resolves to the same artifact as the plain tag
(Context), so choosing it would buy nothing and would imply, to every later reader, that the plain
tag lacks TLS. The `2.x` images link OpenSSL 3 as shipped.

**D3 — The two constants are held equal by a test, not by discipline.**
`TestPinnedImageIsTheOperatorDefault` asserts
`builder.DefaultImage == testimages.MosquittoImage`. It lives in
[`test/imagetools/image_tools_test.go`](../../test/imagetools/image_tools_test.go) behind the
`imagetools` build tag, runs from `make test-image-tools`, and has its own CI job
(`mosquitto-image-tools`, "Mosquitto Image Tools"). If the two ever drift, the job that checks the
image turns red rather than checking the wrong image quietly.

**D4 — One Renovate customManager updates both copies, so they move together.**
The `customManagers` entry in [`renovate.json`](../../renovate.json) whose description begins
"Update the Mosquitto broker image pinned in Go sources" lists both
`/^test/testimages/images\.go$/` and `/^internal/builder/statefulset\.go$/` in its
`managerFilePatterns`, with `versioningTemplate: "docker"` and a single `matchStrings` regex that
anchors on the `renovate:` comment line. Both copies therefore land in one pull request. That
matters because of D3: a manager that moved only one would open a pull request that is red by
construction.

**D5 — An inert regex is a CI failure, not a silent stall.**
[`hack/verify-ci-references.mjs`](../../hack/verify-ci-references.mjs) walks every
`customManagers` entry, resolves its `managerFilePatterns` against the real file list, runs every
`matchString` over the selected files, and fails when a pattern selects no file, when a
`matchString` matches nothing, or when a match captures no `currentValue`. It runs from
`make verify-ci-references`. This is the check that catches the specific failure mode of a
regex-based pin: a constant that is renamed or reformatted stops matching, Renovate stops opening
pull requests, and nothing anywhere reports a problem — the pin just quietly stops being a pin.

**D6 — The pin stays on the 2.x line, and moving off it is a decision somebody makes here.**
The packageRule described as "Mosquitto test image: keep the pin on the 2.x line" matches
`custom.regex` managers with `matchDepNames: ["eclipse-mosquitto"]` and sets
`allowedVersions: "<3"`. Docker versioning keeps the `-alpine` suffix as the compatibility field,
so the constraint still applies to a suffixed tag. A second guard sits in the source:
`TestMosquittoImageIsPinnedToATag` splits the constant on `:`, requires the repository to be
`eclipse-mosquitto`, and requires the tag to start with `2.` — so a hand edit to a 3.x tag fails a
plain `go test`, without waiting for Renovate to be involved at all. The same test also refuses a
digest-only or tagless reference, which is the shape that would stop D4's regex matching.

**D7 — 2.1 is chosen over 2.0 because a config generator that starts on 2.0 emits options that
3.0 removes.**
Mosquitto 2.1 deprecates `per_listener_settings`, `acl_file` and `password_file` in favour of the
`acl-file` and `password-file` plugins, and all three are removed in 3.0. This operator's whole job
in that area is to *generate* configuration into every user's cluster. Starting on 2.0 would mean
generating soon-to-be-removed options and then running a migration later, with users attached.
Starting on 2.1 means the deprecation is visible now, while the generator emits none of the three:
`GenerateMosquittoConf` renders logging, persistence, one `listener`, `allow_anonymous true` and —
under `IsTLSEnabled()` — `certfile` and `keyfile`, and nothing else. **The rule this fixes for the
future: when authentication or ACL support is added, it emits the plugin form.** That obligation
is already written into the doc comment on `MosquittoSpec` and into the generator's own comment,
so it is in front of whoever adds the field.

**D8 — The image check asserts only the binaries this repository actually executes.**
`TestImageProvidesEveryExecutedTool` builds the required list as
`brokerCommand(t)[0]` plus `clientTools = []string{"mosquitto_pub", "mosquitto_sub"}`.
`brokerCommand` calls `builder.BuildStatefulSet` and reads the container's `Command`, so the check
follows a change to that command instead of probing a path that is no longer used. The image also
contains `mosquitto_passwd` and `mosquitto_ctrl` (Context), and the check deliberately does not
assert them: nothing here runs them, and an assertion on an unused binary is a false constraint on
the upstream image.

## Consequences

* **A Renovate bump of the broker image is now a code change that CI can reject on its merits.**
  The image-tools job pulls the proposed image and asks it for
  `/usr/sbin/mosquitto`, `mosquitto_pub` and `mosquitto_sub` by name, so a rebase that moves or
  renames the broker binary is caught on the pull request rather than as a `CrashLoopBackOff` in a
  user's cluster.
* **The pin is enforced in four places that must stay consistent**: two Go constants, one Renovate
  customManager, one packageRule, one equality test and one tag-shape test. That is a lot of
  machinery for a version string, and it exists because every one of them guards a different silent
  failure — drift, an inert regex, an unwanted major, a digest-only reference.
* **Anyone can bypass the pin per resource.** `ResolveImage` returns `m.Spec.Image` whenever it is
  non-empty, with no validation of what it names. A `Mosquitto` can run a 1.x image, a fork, or
  something that is not Mosquitto at all, and the operator will build a StatefulSet for it. The
  generated `mosquitto.conf` and the `/mosquitto/config`, `/mosquitto/data` and `/mosquitto/tls`
  paths assume the 2.x layout; nothing checks that assumption against `spec.image`.
* **`E2E_MOSQUITTO_IMAGE` can also bypass it in CI**, by design, via `testimages.Default()`. The
  workflow deliberately carries no second copy of the pin — a copy that lagged would turn a leg
  green while testing the wrong image.
* **2.1's `max_packet_size` default is inherited silently, and the CRD does not expose it.**
  Mosquitto 2.1 lowers the default from the 256 MB MQTT protocol limit to 2,000,000 bytes. The
  rendered CRD has exactly seven `spec` properties — `antiAffinity`, `config`, `image`, `replicas`,
  `resources`, `storage`, `tls` — and `max_packet_size` occurs nowhere in this repository. **So a
  client publishing a payload between 2 MB and the MQTT maximum is disconnected by a default no
  field in this API mentions.** The only way to change it today is `spec.config`, which is appended
  verbatim and validated by nothing. Making it a named field with an explicit default is open work,
  not done here.
* **The image-tools tier needs Docker and no cluster**, which is why it has its own build tag and
  its own job. It answers in seconds and names the missing binary; the alternative is finding out
  minutes later from a StatefulSet that will not converge.
* **The pin buys nothing about supply chain.** A tag is not a digest: `2.1.2-alpine` can be
  repushed upstream and every future pull silently gets different bytes. This decision pins a
  *version*, not an *artifact*.

## Alternatives Considered

### `eclipse-mosquitto:2.0.22-openssl`

Rejected on measurement. It is the same artifact as `2.0.22` (identical registry digest), and the
plain image already links OpenSSL 3. The tag suggests a capability difference that does not exist,
so using it would encode a false belief into the repository.

### Stay on the 2.0 line (`2.0.22`)

Rejected by D7. 2.0 does not warn about `per_listener_settings`, `acl_file` or `password_file`, so
a generator built against it would emit options that 3.0 removes — into every user's cluster, and
the migration would land later, on running systems. The measurement also removes the usual reason
to prefer the older line: 2.0.22 and 2.1.2-alpine are equally TLS-capable and carry the same tool
set.

### A floating tag: `latest`, `alpine`, or `2`

Rejected. All three currently resolve to the same digest as `2.1.2-alpine` (Context), so today they
would even be correct — which is exactly the problem. They would move on their own, and the first
warning would be a broker behaving differently after a pod restart nobody connected to a version
change.

### A digest pin (`eclipse-mosquitto@sha256:…`)

Not taken. It is strictly stronger against a repushed tag, and it is the honest answer to the
supply-chain consequence above. It loses on readability — the constant stops naming a version a
human can reason about — and it breaks `TestMosquittoImageIsPinnedToATag` and the `currentValue`
capture the Renovate regex depends on. Revisiting it means reworking D4 and D6 together, which is
why it is a separate decision rather than a tweak.

### One constant, imported by the other package

Rejected. `test/testimages` would then depend on `internal/builder` or the reverse, and the two
pins answer different questions — what a user's broker runs, and what CI provisions. Keeping them
separate lets them *differ* deliberately during an upgrade; `TestPinnedImageIsTheOperatorDefault`
is what makes an *accidental* difference loud.

### Let Renovate move the image without a version constraint

Rejected. `allowedVersions: "<3"` exists because a 3.0 bump is not a dependency update here: the
generator, the mount paths and the image check all assume the 2.x layout, and 3.0 removes the
options 2.1 deprecates. That belongs in a pull request a human opens.

### Assert every binary in the image

Rejected by D8. `mosquitto_passwd` and `mosquitto_ctrl` are present but unused; asserting them
would fail a bump for a reason that does not affect this repository.

## Residual risks

* **The digest table records a relationship, not an immutable fact.** The registry digests above
  were read on 2026-09-01 and will change the next time upstream repushes any of those tags. What
  the ADR relies on is the *equality* between `2.0.22` and `2.0.22-openssl`, which was observed in
  two independent measurements that day.
* **Three kinds of hash exist for one tag and they are easy to confuse.** The table quotes index
  digests. The per-platform image manifests inside an index have their own digests, and a pulled
  image has a local image ID on top of that; on `arm64/linux` those IDs were `sha256:5fef2509a20f…`
  for the `2.0.22` pair and `sha256:0db28dca6f11…` for `2.1.2-alpine`. Every one of those is a real
  number for the same tag. **A reader comparing hex across kinds will find a mismatch that is an
  artifact of the command they ran, not a contradiction in this ADR.** The `Built from` column is
  the claim that does not depend on any of them.
* **The in-image checks were run on `arm64/linux` only.** `eclipse-mosquitto` is multi-arch, and CI
  runs on self-hosted runners whose architecture is not asserted anywhere in this repository. The
  binary list and the OpenSSL linkage were not verified on `amd64`.
* **The 2.1 deprecations and the `max_packet_size` change are upstream claims, not measurements.**
  Nothing in this repository exercises `per_listener_settings`, `acl_file`, `password_file` or a
  large payload, and no test asserts the 2,000,000-byte boundary.
* **`max_packet_size` is open work.** It is a real behavioural difference from 2.0 that no field in
  this API names.
* **The pin is a tag, not a digest.** See the last consequence and the digest-pin alternative.
* **Nothing here has run against a real cluster.** `make test-image-tools` needs only Docker, and
  it was the tier exercised for this ADR; the E2E tier, which is where a pinned image actually
  serves MQTT, is unrun evidence from this repository's point of view.

## References

* [`internal/builder/statefulset.go`](../../internal/builder/statefulset.go) — `DefaultImage`,
  `ResolveImage`, the broker `Command`
* [`test/testimages/images.go`](../../test/testimages/images.go) — `MosquittoImage`,
  `EnvMosquittoImage`, `Default`
* [`test/testimages/images_test.go`](../../test/testimages/images_test.go) —
  `TestMosquittoImageIsPinnedToATag`
* [`test/imagetools/image_tools_test.go`](../../test/imagetools/image_tools_test.go) —
  `TestPinnedImageIsTheOperatorDefault`, `TestImageProvidesEveryExecutedTool`, `clientTools`
* [`renovate.json`](../../renovate.json) — the broker-image `customManagers` entry and the
  `allowedVersions: "<3"` packageRule
* [`hack/verify-ci-references.mjs`](../../hack/verify-ci-references.mjs) — the inert-regex check
* [`Makefile`](../../Makefile) — `test-image-tools`, `verify-ci-references`
* [`.github/workflows/release.yml`](../../.github/workflows/release.yml) — the
  `mosquitto-image-tools` job
* [`internal/builder/configmap.go`](../../internal/builder/configmap.go) —
  `GenerateMosquittoConf`, which emits none of the options 2.1 deprecates
* [`api/v1/mosquitto_types.go`](../../api/v1/mosquitto_types.go) — the plugin-form obligation on
  `MosquittoSpec`, and the absence of a `max_packet_size` field
* [ADR 0001](0001-the-operator-consumes-tls-material-it-never-issues-it.md) — the TLS material the
  OpenSSL 3 linkage in this image is there to serve
* [README.md](../../README.md) — the pinned image as users see it
