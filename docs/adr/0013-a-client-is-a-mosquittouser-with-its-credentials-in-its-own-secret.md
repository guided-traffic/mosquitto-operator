# ADR 0013: A Client Is a `MosquittoUser` With Its Credentials in Its Own Secret

## Status

Accepted. Date: 2026-10-05. Decided by the owner, one question at a time, for requirements R1
and R2 of [ADR 0012](0012-the-first-release-is-one-broker-run-from-git-and-high-availability-is-parked.md).
**Not built:** `api/v1/` declares the `Mosquitto` kind only. The measurements D4, D5 and D6 rest
on are in [docs/developer/broker-behaviour.md](../developer/broker-behaviour.md) (M13, M14, M17,
and M21 for D5's characters, measured 2026-10-05).

## Context

A broker serves many clients, each with its own credential and its own rights, and the clients
arrive and leave on a different rhythm than the broker. In a Flux repository each client is
typically its own application — Home Assistant, Zigbee2MQTT — which needs the same username and
password the broker checks, ideally from the same Secret.

Three facts shape the object model. Edit rights on a `Mosquitto` are total control of that broker,
because `spec.config` is appended to its configuration
([ADR 0008](0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md) D10).
Under Flux, one broken object must not hold back unrelated ones: a health check or a `dependsOn`
on a broker that is unready because one client's Secret is missing blocks every application
behind it. And the MQTT username is fixed in each client's existing configuration, so a migration
cannot rename it to fit Kubernetes naming rules.

## Decision

**D1 — A client is a namespaced `MosquittoUser` that names its broker, and everything it names
lives in its own namespace.** `spec.brokerRef.name` names a `Mosquitto`; the reference points from
the user to the broker, so adding a client never writes the broker object. **No reference in this
API carries a namespace field** — not `brokerRef`, not `credentialsSecret` — so a user, its broker,
its Secret and its ACLs are always in one namespace, and a reference across namespaces cannot be
written, rather than being written and refused. The operator acts cluster-wide; each `Mosquitto`
and each `MosquittoUser` acts only inside its namespace. The kind lives in `mko.gtrfc.com/v1` (D9).

**D2 — The username and the password come from the user's own Secret, under configurable keys.**

```yaml
spec:
  brokerRef:
    name: broker
  credentialsSecret:
    name: z2m-mqtt
    usernameKey: username   # default
    passwordKey: password   # default
```

The defaults are the keys of the built-in `kubernetes.io/basic-auth` Secret type, so a basic-auth
Secret works without key configuration and one Secret serves the broker and the client. Field
names are as above; the exact Go types are fixed when the kind is built. Because the effective
username is invisible in the CR, the operator writes it to `status.username` — a username is not a
credential. Editing the username key changes the login; renaming the object does not. Which Secret
of the namespace may be named is the install-time switch of [ADR 0014](0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md) D10: by default any, with
`secretSecurity: true` only one that carries the opt-in label.

**D3 — An ACL entry is a topic and an access mode, mirroring `acl-file`.**

```yaml
acls:
  - topic: zigbee2mqtt/#
    access: readwrite      # read | write | readwrite
  - topic: homeassistant/#
    access: write
```

`read` is subscribe and receive, `write` is publish. No `pattern` entries with `%u`/`%c` yet —
not needed for the migration, additive later. No `deny`: `acl-file` has no priorities to express
"all of `home/#` except `home/alarm/#`" (whether it supports a `deny` access type at all is not
measured).

**D4 — No user may hold a `$` topic.** Every ACL entry whose topic starts with `$` is refused, in
CRD validation (CEL) as a shape check and again at render time as the authority — the split
[ADR 0009](0009-delete-only-through-owner-references.md) draws for object writes. A refused entry
makes the user `Ready=False`; it is never silently dropped. No deny line is rendered: an ACL of
`#` reaches no `$` topic in either direction (M13), so the refusal alone closes `$SYS` and
`$CONTROL`. This is what keeps a later dynamic-security mode safe, where write on
`$CONTROL/dynamic-security/#` is administrative control. Monitoring through `$SYS` goes through
the operator's own reserved user ([ADR 0002](0002-the-metrics-exporter-is-written-here.md) D4).

**D5 — The username is checked at render time against an allowlist and a reserved prefix.** It
must match `^[A-Za-z0-9][A-Za-z0-9._@-]{0,127}$` — an allowlist, so a character nobody thought
of is refused rather than written into a file whose format uses `:` and line breaks, and so `+`,
`#` and `/` can never reach a future `%u` pattern. The prefix `mko-` is reserved for principals
the operator renders itself and refused case-insensitively. Both checks run at render time only,
because the username lives in a Secret that CEL cannot see; a violation is `Ready=False` with a
reason.

**D6 — When two users of one broker resolve to the same username, the oldest wins.** The
`MosquittoUser` with the earliest `creationTimestamp` (ties by object name) is rendered; every
other one is `Ready=False` with a reason naming the holder. A new object can therefore never take
over a running client's identity. The rule is derived from the objects alone and uses no status as
memory. Accepted edge: an older user whose Secret is edited to a younger user's name takes that
name over — not a new exposure, because user and Secret share one namespace (D1) and whoever can
edit that Secret can already rewrite the younger user's password directly. One username is one connection
([ADR 0008](0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md) D14).

**D7 — No roles yet.** A `MosquittoRole` — a named ACL set referenced by many users, mirroring the
dynamic-security object model — is the planned additive step (`roleRefs` beside the inline list),
not part of the first release.

**D8 — Rendering is deterministic: one function, output sorted by username.** A renamed object with
an unchanged login leaves the rendered material byte-identical, so a pass that changes nothing
sends the broker no signal. The test renders one input a hundred times in shuffled order and
expects one distinct output; per [ADR 0010](0010-a-check-is-not-a-check-until-it-has-failed-on-purpose.md)
it is observed failing once against a renderer with the sort removed.

**D9 — Everything stays in `v1`, and `v1` is unstable until operator release 1.0.0.** The README
says so. No `v1alpha1` and no conversion webhook — a webhook needs a serving certificate, which
[ADR 0001](0001-the-operator-consumes-tls-material-it-never-issues-it.md) keeps out of this project.
No commit of this work carries `BREAKING CHANGE` or `!`: the project stays on 0.x.

**D10 — A user reports its own state.** Each `MosquittoUser` carries `observedGeneration` and one
`Ready` condition whose reason says what is wrong — missing Secret or key, broker not found,
refused username, collision, refused topic. Reason strings are fixed when the
kind is built. A user's failure never changes the broker's readiness.

## Consequences

- Two more objects per client in Git (the user and its Secret), and a second kind for the
  reconciler to index and watch.
- Validation is split: what CEL can see is refused at `kubectl apply`; what lives in a Secret is
  refused at render time, visible only in status. A user has to look at the `MosquittoUser`'s
  conditions, not at the apply.
- The migration keeps its usernames, and the client applications keep reading the same Secret.

## Alternatives Considered

- **Users inline in `Mosquitto`.** One object per broker, but a missing Secret either makes the
  whole broker unready — blocking every `dependsOn` on it — or hides the broken user from Flux; and
  the right to add a client would be the right to rewrite `spec.config`. Lost.
- **Roles now, or roles and groups.** Same object graph as dynamic security, but not needed for the
  migration. Deferred, additive.
- **Username from `metadata.name`, or an optional `spec.username` defaulting to it.** The first
  cannot express `Zigbee_2MQTT`; the second couples the login to the object name, so a tidy-up
  rename in Git silently changes a client's login. Lost to the Secret key.
- **A denylist of characters.** Every character nobody thought of passes, and tightening it later
  is breaking. Lost.
- **Collision: all claimants refused, or the newest wins.** The first lets anyone break a running
  client; the second lets anyone take over its identity. "The current holder keeps it" closes the
  older-user edge but needs status as memory, for no gain while everything shares one namespace
  (D1). Lost.
- **Separate `publish` and `subscribe` lists.** Readable for one-way clients, but `readwrite` has
  to be merged at render time, and moving to D3 later would be breaking. Lost.
- **`v1alpha1` for the new kind.** Honest about maturity, but forces an `apiVersion` change in every
  manifest when it graduates; the README line is cheaper while the maintainer is the only user.
  Lost, with the trigger "the first user outside the maintainer".

## Residual risks

- ~~Not measured: that the `passwd` and `acl` parsers accept usernames containing `@` and `.`.~~
  *(Measured 2026-10-05, M21: both parsers accept `@`, `.`, `_` and `-`, and enforce the ACL per
  user under those names.)*
- Not measured: how shared subscriptions (`$share/<group>/<topic>`) are checked against ACLs. If
  the check uses the `$share` form, D4 blocks them; no current client is known to need them.
- The older-user edge of D6, accepted above.
- References across namespaces do not exist in this API (D1). Adding a namespace field to any
  reference would be an amendment of D1, and D6's rule, the topic-prefix question and the hash
  strength of [ADR 0014](0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md)
  D3 would be re-decided with it.

## References

- [ADR 0012](0012-the-first-release-is-one-broker-run-from-git-and-high-availability-is-parked.md) — R1, R2
- [ADR 0014](0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md) — how what this record describes reaches the broker
- [ADR 0008](0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md) Group C — the listener these users log in to
- [docs/developer/broker-behaviour.md](../developer/broker-behaviour.md) — M13, M14, M17
