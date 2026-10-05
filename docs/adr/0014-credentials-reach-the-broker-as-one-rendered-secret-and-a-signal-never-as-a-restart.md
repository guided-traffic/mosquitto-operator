# ADR 0014: Credentials Reach the Broker as One Rendered Secret and a Signal, Never as a Restart

## Status

Accepted. Date: 2026-10-05. Decided by the owner, one question at a time, for requirement R3 of
[ADR 0012](0012-the-first-release-is-one-broker-run-from-git-and-high-availability-is-parked.md);
the mechanism was re-negotiated twice in the same session — once when the owner ruled out a
Secret per user password, once when the owner allowed the operator to read Secrets — and every
step rests on measurements against the pinned image, recorded in
[docs/developer/broker-behaviour.md](../developer/broker-behaviour.md).

**Not built.** The operator holds no `secrets` rule today and renders one container per broker
pod. Before the code that depends on them is written, these are measured: a `$7$` line rendered in
Go is accepted by the broker, byte format identical to `mosquitto_passwd`; a sidecar as uid `1883`
without capabilities can signal the broker across `shareProcessNamespace` under PodSecurity
`restricted`; how long the kubelet takes to refresh a changed Secret volume, and whether it swaps
`tls.crt` and `tls.key` together; what the broker does with a mismatched TLS pair at start.

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
([ADR 0002](0002-the-metrics-exporter-is-written-here.md) D4). There is no Secret per user
password. It is written only after `ensureOwned`, owned through a controller reference, and
collected with its `Mosquitto` ([ADR 0009](0009-delete-only-through-owner-references.md)). Because
it lies in the same namespace as the plaintext Secrets it is rendered from, whoever can read it
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
Secret propagation plus the sidecar — not measured, documented once it is.

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

## Residual risks

- The default `all` grant, accepted above.
- The entry measurements in Status. Until they run, D3's format, D4's signal and D8's latency are
  claims from documentation and from the rig, not from a cluster.
- No kick for a client whose credential is still valid; D9's trigger.
- A pod compromise exposes the hashes in the `emptyDir`; at 1000 iterations they are cheap to
  attack offline. Accepted with D3.

## References

- [docs/developer/broker-behaviour.md](../developer/broker-behaviour.md) — M2–M8, M12, M14, M18
- [ADR 0013](0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md) — what is rendered
- [ADR 0006](0006-both-install-paths-grant-the-same-authority.md) D9 — the grant table
- [ADR 0001](0001-the-operator-consumes-tls-material-it-never-issues-it.md) D10 — the TLS reload
- [ADR 0008](0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md) Group C — the listener
