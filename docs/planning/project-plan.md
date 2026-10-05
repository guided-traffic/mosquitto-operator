# Project plan

How `v0.1.x` becomes the operator the Home Assistant migration needs: one broker per `Mosquitto`,
run from Git through Flux, with users, permissions and credentials that follow every change on
their own ([ADR 0012](../adr/0012-the-first-release-is-one-broker-run-from-git-and-high-availability-is-parked.md)).
Phases, not dates. Each phase names what it delivers, how that is verified, which records it
builds, and its effort. A phase that starts becomes a family ticket and its children under
[docs/tickets/](../tickets/README.md), in a session dedicated to that conversion; a phase that ends
is deleted from this file
([ADR 0011](../adr/0011-documentation-has-five-homes-and-tickets-are-work-lists-that-get-archived.md) D10).

Written 2026-10-05.

## Working agreements

- **Decide before building.** Every decision a phase needs is a record already; a question that
  comes up while building goes into the phase's ticket as an open question and is put to the owner
  one at a time. Code written on an undecided question is speculation.
- **Measure before relying.** A phase whose design rests on broker behaviour starts with the
  measurements it names; each result goes into
  [docs/developer/broker-behaviour.md](../developer/broker-behaviour.md) with its command and its
  output, and a result that contradicts a record stops the phase until the record is amended.
- **No phase closes without its tests** in the cheapest tier that can answer the question — and
  never in a tier that cannot (envtest starts no pod and collects no garbage) — and without the
  pages under `docs/` and the README reference that describe what it built.
- **Every new guard has failed on purpose** before it is trusted, with the message recorded
  ([ADR 0010](../adr/0010-a-check-is-not-a-check-until-it-has-failed-on-purpose.md)).
- **Verification is named.** "Done" in a ticket means what was run, against what, with what result.
  Nothing in this repository has yet been observed running against a real cluster; phase 5 is
  where that changes.
- **The project stays on 0.x.** No commit carries `BREAKING CHANGE` or `!`, even where the
  behaviour breaks ([ADR 0013](../adr/0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md) D9).

## Done

`v0.1.0` to `v0.1.8`: one `Mosquitto` renders a ConfigMap, a headless and a client Service and a
StatefulSet; TLS from an existing Secret; anonymous brokers; two install paths with equal
authority; five test tiers and the CI that runs them. What it built is in the records 0001 to 0010
and in [docs/developer/](../developer/README.md).

## Phase 1 — Pod labels, the configuration gate and the admission guard

**Goal:** the small, independent half of the release — R4 and R6 of ADR 0012 — shipped before
anything touches authentication.

**Builds:** [ADR 0012](../adr/0012-the-first-release-is-one-broker-run-from-git-and-high-availability-is-parked.md)
D4 and D5, [ADR 0007](../adr/0007-one-broker-image-pin-and-why-not-the-openssl-tag.md) D9 and D10,
[ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md) D10 for the TLS Secret.

**Delivers:**

- The E2E jobs of [`release.yml`](../../.github/workflows/release.yml) restored — commented out
  since 2026-09-01, with `e2e-tests` back in the `needs:` of `semantic-release` — so that every
  E2E verification this plan names runs on every pull request, not only by hand.
- `spec.podLabels` and `spec.podAnnotations`, merged **under** the operator's own keys; the CRD,
  `make generate-all`, the chart's CRD copy, the README reference.
- The `--test-config` init container from the broker image, with the broker container's security
  context, run against the mounted configuration.
- The supported broker line (2.1.x) stated in the README and in the `image` field description.
- The `secretSecurity` switch, default `false`, as a chart value and an operator flag: with `true`
  a TLS Secret without the opt-in label is refused; the README states the trust rule of `false`
  next to the install command, and H-15 on the security pages names the switch.
- The restricted-admission guard: a rendered broker pod admitted by a real API server in a
  namespace labelled `pod-security.kubernetes.io/enforce=restricted` — an integration test that
  creates a Pod from the built template if envtest's API server enforces PodSecurity (it runs no
  StatefulSet controller, so the Pod has to be created directly), the E2E tier otherwise.

**Verified when:**

- a unit test tries to overwrite `app.kubernetes.io/instance` and a hash annotation through
  `podLabels`/`podAnnotations` and the operator's values win;
- an E2E test changes a pod label on the CR and observes the StatefulSet roll and the new pods carry
  it;
- an E2E test applies a `spec.config` typo and finds the init container's message with file and
  line in its log, the broker container never started;
- the admission guard passes, and was observed failing with the API server's own message against a
  security context with `allowPrivilegeEscalation: true`;
- a unit test refuses an unlabelled TLS Secret with `secretSecurity: true` and accepts it with
  `false`, observed failing once with the check removed.

**Not verified yet, and settled here:** whether envtest's API server enforces PodSecurity labels.

**Effort:** M.

## Phase 2 — The measurements the user phase stands on

**Goal:** every claim phase 3 builds on is measured against the pinned image, so phase 3 starts
on facts.

**Builds:** the Status sections of [ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md),
[ADR 0013](../adr/0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md) and
[ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md) D10.

**Delivers**, each as a section of [broker-behaviour.md](../developer/broker-behaviour.md) with
command and output:

| # | Measurement | Rig |
|---|---|---|
| E1 | A `$7$1000$…` line rendered by Go code is accepted by the broker, byte format identical to `mosquitto_passwd` | docker |
| E2 | The `passwd` and `acl` parsers accept usernames containing `@` and `.` | docker |
| E3 | A sidecar as uid `1883`, all capabilities dropped, signals the broker across `shareProcessNamespace` under `enforce=restricted` | Kind |
| E4 | How long the kubelet takes to refresh a changed Secret volume, and whether `tls.crt` and `tls.key` change together | Kind |
| E5 | What the broker does with a mismatched TLS pair at start | docker |
| E6 | A 2.0 image fails `--test-config` on the generated file, with file and line | docker |
| E7 | The `spec.config` allowlist, taken from `mosquitto.conf(5)` of 2.1.2 | reading the man page of the pinned version |

E1 needs the hashing code of phase 3 in a minimal form; it is written here and kept.

**Verified when:** each row has its section, and every record whose Status names it says
"measured" with the date — or has been amended because the measurement said otherwise.

**Effort:** S.

## Phase 3 — Users, permissions and a broker that requires a login

**Goal:** R1, R2, R3 and R5 of ADR 0012 — the core of the release.

**Builds:** [ADR 0013](../adr/0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md)
entire; [ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md)
D1–D4, D6–D8 and D10 for `credentialsSecret`; [ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
D13–D16; [ADR 0006](../adr/0006-both-install-paths-grant-the-same-authority.md) D9.

**Delivers:**

- The `MosquittoUser` CRD in `mko.gtrfc.com/v1`: `brokerRef`, `credentialsSecret` with its key
  defaults, the ACL list, CEL refusal of `$` topics, `status.username`, `observedGeneration`, the
  `Ready` condition with its reasons; `make generate-all`, the chart's CRD copy.
- The renderer: one function from the bound users to `passwd` and `acl`, sorted by username,
  `$7$`/1000 with salt and hash kept while the plaintext verifies, the render-time checks (same
  namespace, username allowlist, reserved `mko-`, `$` topics, oldest-wins collisions).
- `<name>-auth`, written after `ensureOwned`, owned by the `Mosquitto`.
- The broker pod: the copy init container, the reload sidecar (`manager` second entry point,
  bytes compared, temp then rename, SIGHUP), `shareProcessNamespace: true`, all containers as uid
  `1883`.
- The generated listener: both plugins, `listener_allow_anonymous false`,
  `use_username_as_clientid true`, no `allow_anonymous`; the `spec.config` allowlist at render
  time with the previous configuration kept on refusal.
- Watches on `MosquittoUser` and on the referenced Secrets, mapped back to the broker; the broker
  without users reporting that it accepts nobody.
- RBAC: the markers, `config/rbac/role.yaml`, the chart's hand-written ClusterRole, the secret
  mode as a chart value and a kustomize component per mode, default `all`, the parity test per
  mode; `NOTES.txt` and the README stating the grant before the install command; the operator's
  own image passed to it on both paths.
- The README reference for the new kind, the new fields and the chart value; the pages under
  `docs/operations/`, `docs/security/` (privilege footprint, credentials, validation) and
  `docs/developer/` for what was built; the Status of every record above set to built.

**Verified when:**

- unit: render determinism (a hundred shuffled renders, one output, observed failing without the
  sort), salt preservation, every render-time refusal including a `$` entry that bypassed CEL, the
  oldest-wins rule, the allowlist refusing `listener` and `connection`;
- integration: the CRD is accepted, CEL refuses a `$` topic at apply, owner references on
  `<name>-auth` are accepted as written, the parity test passes in both modes;
- E2E — and the first of these is the phase: a `MosquittoUser` created against a running broker can
  publish without the broker pod restarting; a client is refused on a topic outside its ACL; a
  changed password in the user's Secret makes the old password fail and the new one work with no
  edit to any CR; a deleted user's live connection is dropped; an anonymous client is refused; a
  second user with another user's client ID does not take over its session; a `spec.config` with a
  second `listener` is refused and the broker keeps serving.

**Effort:** L.

## Phase 4 — Certificates renewed in place

**Goal:** the TLS half of R3 — a cert-manager renewal reaches a running broker without a restart.

**Builds:** [ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md) D10,
[ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md)
D5.

**Delivers:** the sidecar watching the TLS mount, the cert/key pair check before every signal, the
sidecar present whenever `spec.tls` is set even without users; the rotation pages under
`docs/security/` and `docs/operations/` rewritten from "restart" to "reload".

**Verified when:** an E2E test renews a cert-manager certificate and a fresh `openssl s_client`
sees the new serial with no pod restart, while a subscriber connected before keeps receiving; and
a Secret edited to a mismatched pair leaves the broker serving the previous certificate.

**Effort:** S.

## Phase 5 — The Home Assistant migration

**Goal:** the release does what ADR 0012 D1 says, on a real cluster — the first observation of
this operator running anywhere but CI.

**Delivers:** a Flux example in the README — a `Mosquitto`, a `MosquittoUser` and a SOPS-encrypted
basic-auth Secret per client, the Flux `Kustomization` with a health check on both kinds —; a
user-written NetworkPolicy example against the selector labels
([ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
D16); the measured revocation latency (E4) in the operations pages; the owner's migration of Home
Assistant and its clients.

**Verified when:** the owner's Home Assistant and Zigbee2MQTT run against the new broker, applied by
Flux; a password rotated in Git reaches both without a manual step; the owner records what was run
and what was observed.

**Effort:** S, plus the owner's time.

## Phase 6 — The exporter

**Goal:** broker metrics for Prometheus.

**Builds:** [ADR 0002](../adr/0002-the-metrics-exporter-is-written-here.md) as amended — the
reserved `mko-exporter` user with `read $SYS/#`, its password a key of `<name>-auth`.

**Verified when:** as ADR 0002 names it, plus an E2E test that the exporter logs in as
`mko-exporter` and that no `MosquittoUser` can claim that name.

**Effort:** M.

## Parked, with what reopens it

| What | Where it waits | Reopened by |
|---|---|---|
| High availability, `replicas > 1` semantics, PDB, upgrade without message loss | [ha-research.md](ha-research.md), questions HA1–HA7 | the owner's call, at the latest a client that needs a bounded failover |
| `MosquittoRole` | [ADR 0013](../adr/0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md) D7 | many users with identical ACLs |
| Dynamic-security mode | [ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md) D9 | ACL priorities, or kicking a client with a valid credential |

## What is deliberately not planned

- Issuing certificates; cert-manager as a dependency at any layer
  ([ADR 0001](../adr/0001-the-operator-consumes-tls-material-it-never-issues-it.md)).
- Admission webhooks and conversion webhooks — each needs a serving certificate.
- A NetworkPolicy shipped by the operator ([ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md) D16).
- Generating client passwords; the users own their Secrets.
- References across namespaces. A `Mosquitto`, its users, their Secrets and their ACLs share one
  namespace; no reference carries a namespace field
  ([ADR 0013](../adr/0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md) D1).
  The operator itself acts cluster-wide.
