# ADR 0014: Credentials Reach the Broker as One Rendered Secret and a Signal, Never as a Restart

## Status

Accepted. Date: 2026-10-05. Decided by the owner, one question at a time, for requirement R3 of
[ADR 0012](0012-the-first-release-is-one-broker-run-from-git-and-high-availability-is-parked.md);
the mechanism was re-negotiated twice in the same session — once when the owner ruled out a
Secret per user password, once when the owner allowed the operator to read Secrets — and every
step rests on measurements against the pinned image, recorded in
[docs/developer/broker-behaviour.md](../developer/broker-behaviour.md).

**Built 2026-10-05:** D1–D8 and D10 — everything but D9, the later dynamic-security mode. Where the build differs from the text, it says so below.

- D1, D2: the generated configuration loads the file plugins; `<name>-auth` (keys `passwd`, `acl`)
  is written after `ensureOwned`, only on a difference, from `auth.Render` and `auth.FilePayload`.
- D3: `auth.HashPassword` / `auth.VerifyPassword`, 1000 iterations, 64-byte salt; a hash is kept
  while its password verifies (`TestRender_KeepsAHashWhileItsPasswordVerifies`). The image accepts
  the operator's hash and the operator the image's, on every pull request (`test/imagetools`).
- D4, D6: `auth-init` and `reloader` run `/app/manager reload` from the operator's image
  (`internal/reloader`), the pod shares its process namespace, the copy is `0600` through a
  temporary file and a rename, read through one resolved `..data`. The image reaches the pod as
  `--reloader-image`: the chart passes its own image; `config/default` copies the manager's image
  into the flag with a `replacements` entry, which works for an image set in `config/manager`
  (`make deploy`, `kustomize edit set image`) — **not** for an `images:` entry in a user's own
  overlay, which runs after `config/default` was built; the installation docs show the patch.
- D7: the markers, the chart's `secretAccess` and the component `secret-namespaces`
  ([ADR 0006](0006-both-install-paths-grant-the-same-authority.md) D9). **Built differently from
  the text in one point, on the recommended answer of an open question in
  [the project plan](../planning/project-plan.md):** the operator caches Secrets with their data,
  annotations and managed fields stripped (`controller.StripSecret`) and reads a credentials
  Secret's data with one uncached `get` — so no Secret data is held in the cache at all — instead of
  D10's "the operator's Secret cache is restricted to labelled Secrets".
- D8: observed on Kind — a new user publishes 67 s after it was created, a changed password locks
  the old one out within 45 s, a deleted user's connection drops within 72 s, nothing restarted
  (`TestE2E_Users_TheBrokerFollowsItsUsers`, run 2026-10-05 against `kind-mko-dev`).
- D5: the `reloader` gets the TLS volume and `--tls-dir` whenever `spec.tls` is set, checks the
  mounted pair with `crypto/tls.X509KeyPair` and signals a changed, valid pair
  (`internal/reloader/tls.go`). **Built more strictly than the text in one point:** while the
  mounted pair is invalid the sidecar sends no signal at all, not for a credential change either,
  because the one SIGHUP reloads both and an invalid pair takes the listener down (M12). Observed
  on Kind: `TestE2E_TLS_ACertManagerRenewalIsReloaded`, `TestE2E_TLS_AMismatchedPairIsNeverLoaded`
  ([ADR 0001](0001-the-operator-consumes-tls-material-it-never-issues-it.md) D10).
- D10 for `credentialsSecret`: the label is checked on the cached metadata before the data is
  read (`TestReconcile_EveryUserReason`, observed failing with the check removed).

D10 for the TLS Secret *(built 2026-10-05, first)*: `--secret-security`, the chart
value `secretSecurity` and the component `config/components/secret-security`, default `false` on
both paths; with `true` the label is `mko.gtrfc.com/consumable=true`, a refusal is `Ready=False`
with reason `SecretNotConsumable` (or `SecretNotFound`), and the only grant added is `get` on
`secrets`, because the read is a metadata-only `get` through the uncached reader rather than a
cached informer — so D10's "the operator's Secret cache is restricted to labelled Secrets" does not
apply yet: there is no Secret cache. `TestReconcile_SecretSecurity` was observed failing with the
check removed; `test/rbacparity` renders both settings and was observed failing on a component
without the rule (`ClusterRole core/secrets: granted by the chart (get) but not by kustomize`) and on
a chart without the flag (`expected: "false"`, `actual  : ""`). Phase 4 superseded that conditional
`get` rule: the Secret grant of D7 carries `get` already, so `secretSecurity` now changes the flag
and nothing else. The four measurements the code depends on were taken on 2026-10-05 and hold the record as
written ([broker-behaviour.md](../developer/broker-behaviour.md)): a `$7$` line rendered in Go is
accepted by the broker, in `mosquitto_passwd`'s exact format (M20); a sidecar as uid `1883`
without capabilities signals the broker across `shareProcessNamespace` under PodSecurity
`restricted`, and another uid cannot (M22, on Kind); the kubelet swaps a changed Secret volume in
one step, `tls.crt` and `tls.key` together, 69 to 84 seconds after the change on an idle Kind node
(M23); and a mismatched TLS pair at start stops the broker with `key values mismatch`, exit 1 (M24).

## Context

Mosquitto 2.1 offers two ways to hold users. The `password-file` and `acl-file` plugins read
files and re-read them on SIGHUP: a user added to the files becomes usable without a restart (M2),
and the reload disconnects exactly the clients whose user was removed or whose password changed,
while a narrowed ACL applies to live connections per message (M14). The dynamic-security plugin
changes immediately over its MQTT control topic (M4) but never re-reads its file on SIGHUP (M3),
rewrites that file itself, takes passwords in plaintext over the network, and needs an admin
credential with a network path from the operator to every broker pod.

Kubernetes cannot produce a file Mosquitto will accept long-term: Secret and ConfigMap volume
files are owned by root, and 2.1 warns that future versions will refuse them (M6). The pinned
image hashes only PBKDF2-SHA512 (`$7$`, 1000 iterations); argon2id fails in it (M18). A renewed
TLS certificate is re-read on SIGHUP, but a mismatched pair breaks every new handshake (M12).

The owner set two constraints during the decision: no Secret is derived per user password, and
the operator may read the users' Secrets.

## Decision

**D1 — The broker holds users through the `password-file` and `acl-file` plugins.** The generated
configuration loads both with `plugin_load`, sets `plugin_opt_password_file` and
`plugin_opt_acl_file` — the only spellings the plugins accept (M7) — and binds them to the
listener with `plugin_use`; the listener block is
[ADR 0008](0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md) D13 and
D14. The operator is the only writer of the files' content; reconciliation is render and compare.

**D2 — One operator-owned Secret per broker, `<name>-auth`, carries the whole rendered state.** It
holds the `passwd` content (hashes only) and the `acl` content for every `MosquittoUser` bound to
the broker ([ADR 0013](0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md)),
plus, later, keys the operator needs for itself
([ADR 0002](0002-the-metrics-exporter-is-written-here.md) D4) — *built 2026-10-05:*
`exporter-password`, projected into the exporter alone; the `auth-secret` mount of `auth-init`
and `reloader` became an `items` projection of `passwd` and `acl`. There is no Secret per user
password. It is written only after `ensureOwned`, owned through a controller reference, and
collected with its `Mosquitto` ([ADR 0009](0009-delete-only-through-owner-references.md)). Because
it lies in the same namespace as the plaintext Secrets it is rendered from — no reference crosses a
namespace ([ADR 0013](0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md) D1) — whoever can read it
could already read the plaintext; the hashes expose nothing that was not already reachable. Because
it holds the full state, a broker pod that restarts while the operator is down comes up with every
user.

**D3 — Passwords are hashed as `$7$` (PBKDF2-SHA512) at 1000 iterations, and a hash is kept while
its plaintext still verifies.** This is what `mosquitto_passwd` of the pinned image produces (M18)
and what every 2.x build verifies, whatever `spec.image` names. No option for more iterations:
their protection applies only to a hash that leaks without its plaintext — practically only from a
compromised broker pod, which reads all traffic anyway — while every failed login against a known
username would cost the broker the full hashing time, from any pod in the cluster
([ADR 0008](0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md) D16).
The salt is random, so the renderer keeps the existing salt and hash whenever the plaintext still
verifies against it; otherwise every pass would change `<name>-auth` and signal the broker forever.

**D4 — The rendered Secret reaches the broker by a copy and a signal; nothing restarts.**
`<name>-auth` is mounted as a whole volume — no `subPath`, so the kubelet refreshes it — and is not
part of the pod-template hash, so a change rolls nothing. An init container copies it into an
`emptyDir` on **every** start, unconditionally, as `1883:1883` mode `0600`, which is the file state
Mosquitto accepts without warning (M6). A sidecar compares the mounted bytes after each kubelet
swap of `..data`, writes the copy to a temporary name and renames it, and sends the broker SIGHUP.
The pod sets `shareProcessNamespace: true` so the sidecar can signal; every container runs as uid
`1883` with all capabilities dropped, so the signal needs no `CAP_KILL`
([ADR 0012](0012-the-first-release-is-one-broker-run-from-git-and-high-availability-is-parked.md) D4).
The pod rolls only for what rolls it today — `mosquitto.conf`, the pod template, the pod labels.

**D5 — The same sidecar reloads the TLS material, after checking it.** It watches the mounted TLS
directory too, and signals only after `tls.crt` and `tls.key` form a valid pair, so a half-edited
Secret leaves the broker on its previous certificate
([ADR 0001](0001-the-operator-consumes-tls-material-it-never-issues-it.md) D10). The sidecar is
present whenever `spec.tls` or a user is configured.

**D6 — The sidecar is a second entry point of the operator binary, shipped in the operator image.**
Go gives it `crypto/tls` for the pair check and unit tests for the copy, the rename and the
signal. Accepted cost: the sidecar's image tag is in the pod template, so **every operator release
rolls every broker** — seconds of MQTT outage; sessions on a PVC survive. The operator learns its
own image from install-time configuration set on both paths.

**D7 — The operator reads the users' Secrets, under an install-time mode whose default is `all`.**
`get;list;watch` reads the password Secrets and wakes the reconciler when one changes;
`create;update` writes `<name>-auth`. The mode is `all` — a ClusterRole rule over every namespace —
or `namespaces`, one Role per listed namespace with the Secret cache restricted to the list;
[ADR 0006](0006-both-install-paths-grant-the-same-authority.md) D9 is the grant table. **The
default is `all`, accepted by the owner after the risk was stated:** a compromised operator can
read and overwrite every Secret in the cluster, including GitOps deploy keys and the SOPS key,
which in a Flux cluster is a path to the whole cluster. The README states the grant before the
install command and the chart's `NOTES.txt` prints it when `all` is active. A `MosquittoUser`
whose namespace the grant does not cover reports `Ready=False` with a reason naming the setting.
The operator still never reads the TLS Secret.

**D8 — Revocation is the reload.** A removed user and a changed password are disconnected on the
reload that carries the change; a narrowed ACL applies at once (M14). The latency is the kubelet's
Secret propagation plus the sidecar — *(measured 2026-10-05, M23:)* the kubelet's part was 69 to 84
seconds on an idle Kind node with default settings; the sidecar's part is not measured yet.

**D9 — Dynamic security is a later, opt-in broker mode, and the first release is built so that it
stays cheap.** `spec.auth.mode: files | dynsec`, default `files`, is added only when needed;
adding it later is non-breaking. Triggers: ACL priorities become necessary, or a client whose
credential is still valid must be kickable. What the implementation keeps true now:

| Constraint | Why it matters for dynsec |
|---|---|
| The `MosquittoUser` API says nothing file-specific ([ADR 0013](0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md) D2, D3) | The same objects must render to dynsec JSON without a user-visible change |
| Rendering is one function from desired state to a mode-specific payload; delivery is a separate step | dynsec swaps the payload and adds a live delivery path over MQTT; the render-and-compare core stays |
| `<name>-auth` stays the single per-broker Secret, keys named per payload | dynsec adds its JSON and the operator's admin credential as further keys — still one Secret, still the start-without-operator property |
| Salt and hash preservation lives in the renderer, not in the file format | dynsec stores salted hashes too |
| The reserved `mko-` prefix is enforced from the first release ([ADR 0013](0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md) D5) | dynsec needs an operator-owned admin principal whose name nobody can claim first |
| The init container copies on every start, unconditionally | in dynsec mode the broker writes its own file; copying on every start keeps the Secret authoritative over a stale PVC copy |

What a dynsec mode adds is recorded so it is not rediscovered: an admin credential with network
reach to every broker pod, TLS on that path, drift detection against live state, the JSON rendered
in Go and never through `mosquitto_ctrl -f` (M5), and three measurements — the 2.1 JSON schema with
its hash fields, whether `kickClient` ends a live session, whether `setClientPassword` drops
existing connections.

**D10 — Which Secret a `Mosquitto` or a `MosquittoUser` may name is an install-time switch,
`secretSecurity`, default `false`.** *(Added 2026-10-05; built 2026-10-05 for both halves; the
cache restriction below built as a cache without Secret data, pending the answer in the plan.)* With `true`, the
operator mounts a TLS Secret and reads a `credentialsSecret` only when the Secret carries an opt-in
label under `mko.gtrfc.com/` (the key is fixed when built); whoever can label a Secret is whoever
can write it, so the label is the Secret owner's consent. A resource naming an unlabelled Secret
reports `Ready=False` with a reason naming the label, and the operator's Secret cache is restricted
to labelled Secrets. With `false`, any Secret of the resource's namespace may be named — the
behaviour of the tree today — and the README and
[docs/security/trust-boundaries.md](../security/trust-boundaries.md#h-15) state the trust rule:
`create` or `update` on `mosquittoes` (and on `mosquittousers`) in a namespace is equivalent to
reading every Secret of that namespace, because the author also chooses the image that runs with
the Secret mounted. It is a chart value and an operator flag, so both install paths offer it and
default to `false`. **The default was chosen by the owner against the recommendation** (`true`),
after the exposure was stated: a cluster that grants `mosquittoes` more narrowly than Secrets is
exposed until its administrator turns the switch on. The finding was published on 2026-10-05 by the
owner's decision, before the switch exists. With `true` in `namespaces` grant mode (D7), a TLS
Secret in a namespace the grant does not cover cannot be checked, and the broker reports
`Ready=False`.

## Consequences

- The largest change to the privilege footprint since `v0.1.0`: from no `secrets` rule to
  cluster-wide read and write by default. The security documentation carries it as such when it is
  built.
- Every broker pod grows an init container and a sidecar and shares its process namespace among
  containers this project builds.
- Every operator upgrade restarts every broker.
- A change in Git reaches the broker with a delay the kubelet decides, not the operator.

## Alternatives Considered

- **Dynamic security now.** Immediate changes and `kick`, but the operator would need every
  plaintext over the network, an admin credential, a network path to every pod and a two-way sync
  against state the broker writes. Lost to the file plugins; kept as D9.
- **Both modes in the first release.** Two renderers, two delivery paths, two test suites, two
  security postures, three unmeasured dynsec behaviours blocking the release. Lost.
- **The user supplies the hash.** No new privilege, but two Secrets per user kept in step by hand —
  the manual step R3 forbids. Lost.
- **One volume per user Secret, hashed inside the pod.** No operator privilege, but adding a user
  changes the pod spec and restarts the broker, and a missing Secret cannot be reported per user.
  Lost.
- **A helper in the pod reading Secrets through the API.** Gives the network-facing pod a token
  that reads every Secret of its namespace. Lost.
- **`pods/exec` into the broker, or the hashes in a ConfigMap.** Near cluster-admin, or hashes
  outside Secret protection. Lost.
- **A `normal | strong` hashing option.** Only more iterations are available (M18); see D3. Lost.
- **A shell script in the broker image as the sidecar.** The image has no `openssl`, so D5's pair
  check would be impossible. Lost.
- **A separate reloader image.** Avoids rolling brokers on every operator release, at the cost of a
  second release pipeline. Lost.
- **`namespaces`, or no Secret access, as the default.** Recommended and rejected by the owner, who
  runs `all` and accepted the risk for third-party installers with the documentation of D7.
- **For D10 — only a type check** (`kubernetes.io/tls`, `kubernetes.io/basic-auth`). Another
  service's Secret of the right type still passes. Lost.
- **For D10 — `true` as the default.** Recommended; the owner chose `false`.

## Residual risks

- The default `all` grant, accepted above.
- D10's default `false`, accepted above: with it, writing a `Mosquitto` or a `MosquittoUser` is
  reading the namespace's Secrets.
- The entry measurements are taken (M20, M22–M24) on a workstation and on one idle Kind node; D8's
latency on a loaded or differently configured kubelet is not measured.
- No kick for a client whose credential is still valid; D9's trigger.
- A pod compromise exposes the hashes in the `emptyDir`; at 1000 iterations they are cheap to
  attack offline. Accepted with D3.

## References

- [docs/developer/broker-behaviour.md](../developer/broker-behaviour.md) — M2–M8, M12, M14, M18
- [ADR 0013](0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md) — what is rendered
- [ADR 0006](0006-both-install-paths-grant-the-same-authority.md) D9 — the grant table
- [ADR 0001](0001-the-operator-consumes-tls-material-it-never-issues-it.md) D10 — the TLS reload
- [ADR 0008](0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md) Group C — the listener
