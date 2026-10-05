# What the pinned broker image actually does

Facts about `eclipse-mosquitto:2.1.2-alpine` — the image the operator runs by default
([ADR 0007](../adr/0007-one-broker-image-pin-and-why-not-the-openssl-tag.md)) — that the
operator's design rests on, each measured rather than read from documentation, with the command
and the broker's own output. Read this before changing anything that generates broker
configuration, renders credential files, or signals a broker: several of these are things a
reasonable person would assume the other way, and the decisions in
[ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md),
[ADR 0013](../adr/0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md) and
[ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md)
follow from them.

**Rig.** Single-container docker 28.4.0 on arm64, the pinned image, configuration and credential
files on bind mounts. M1–M9 were measured on 2026-09-01, M12–M18 on 2026-10-05. **None of it
ran on a cluster.** The numbering has a gap: M10 and M11 measure a bridged broker pair and belong
to the parked high-availability research in [docs/planning/](../planning/), not to anything the
operator builds.

The image changes when Renovate moves the pin; a measurement here describes `2.1.2-alpine` and is
repeated before a decision is rebuilt on a different version.

## M1 — What the pinned image contains

```
/usr/lib/mosquitto_acl_file.so
/usr/lib/mosquitto_dynamic_security.so
/usr/lib/mosquitto_password_file.so
/usr/lib/mosquitto_persist_sqlite.so
/usr/lib/mosquitto_sparkplug_aware.so
/usr/bin/mosquitto_ctrl  /usr/bin/mosquitto_passwd
/usr/bin/mosquitto_pub   /usr/bin/mosquitto_sub  /usr/bin/mosquitto_rr
```

All five 2.1 plugins ship in the image. No custom image is needed for the file plugins or for dynamic security.

## M2 — The file plugins reload on SIGHUP

Broker running with `password-file` and `acl-file` plugins and one user `alice`. A second user
`bob` was appended to both files while the broker ran:

```
--- bob BEFORE sighup ---   rejected
--- bob AFTER  sighup ---   accepted
```

**A user added to the files becomes usable after `kill -HUP`, with no restart and no dropped
connections.** The plugin binaries carry `password_file__reload` and `acl_file__reload` symbols,
so this is the designed behaviour and not an accident of caching.

## M3 — Dynamic security does *not* re-read its file on SIGHUP

Same experiment against `mosquitto_dynamic_security.so`: a client was created over MQTT, the JSON
on disk was then edited to rename that client, and SIGHUP was sent. The broker logged
`Reloading config.` and **kept serving the old in-memory state** — the renamed client was still
accepted under its old name.

Consequence: with dynsec, a file the operator renders is read exactly once, at process start.
Every user change is either a broker restart or a live MQTT command. There is no declarative
middle path.

## M4 — Dynamic security changes over MQTT are immediate

Creating a client, a role and its ACLs through `mosquitto_ctrl ... dynsec ...` against the running
broker made that client usable at once, no restart. **The control-plane path works**; it is the
file path that does not. This is what keeps dynsec a live option rather than a rejected one.

## M5 — `mosquitto_ctrl -f <file>` is a trap

Offline file mode is documented for `dynsec init` and `dynsec setClientPassword` only. Anything
else **exits 0 and silently does nothing**:

```
mosquitto_ctrl -f ds.json dynsec createClient sensor1 -p pw1 ; echo rc=$?
rc=0
grep -o '"username":"[^"]*"' ds.json
"username":"admin"          <- sensor1 was never created
```

An implementation that shells out to `mosquitto_ctrl -f` to render dynsec state would report
success and produce an empty ACL set. If dynsec is ever built ([ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md) D9), the JSON has to be rendered
in Go from the documented schema, not through the CLI.

## M6 — Kubernetes cannot produce a file Mosquitto will accept in future versions

Same broker, same config, three file states:

| Owner / mode | Broker output |
|---|---|
| `root:mosquitto`, `0644` — what a Secret volume produces | `Warning: File ... has world readable permissions. Future versions will refuse to load this file.` **+** `Warning: File ... owner is not mosquitto.` |
| `root:mosquitto`, `0640` — `defaultMode` plus `fsGroup: 1883` | `Warning: File ... owner is not mosquitto. Future versions will refuse to load this file.` |
| `mosquitto:mosquitto`, `0600` | clean |

Kubernetes writes Secret and ConfigMap volume files owned by **root**; `fsGroup` sets the group and
never the owner. **The clean row is unreachable from a projected volume.** Direct mounting works on
2.1 and is on a stated path to breaking, which makes the copy into an emptyDir ([ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md)) a
requirement rather than a nicety.

Related, and the reason the first attempt at M2 failed: the broker drops privileges to user
`mosquitto`, and a root-owned `0640` file produced
`Error loading Dynamic security plugin config: File is not readable - check permissions.`

## M7 — Plugin option names, and the one that works

Brute-forced against the running broker. Only one spelling is accepted:

```
plugin_opt_password_file  =>  Plugin password-file loaded.
plugin_opt_passwordfile   =>  password-file: Error: Unknown option 'passwordfile'.
plugin_opt_pwfile         =>  password-file: Error: Unknown option 'pwfile'.
plugin_opt_filename       =>  password-file: Error: Unknown option 'filename'.
plugin_opt_path           =>  password-file: Error: Unknown option 'path'.
plugin_opt_file_name      =>  password-file: Error: Unknown option 'file_name'.
plugin_opt_pw_file        =>  password-file: Error: Unknown option 'pw_file'.
```

The 2.1 shape, which is also what replaces the deprecated `per_listener_settings`:

```
plugin_load  pwfile /usr/lib/mosquitto_password_file.so
plugin_opt_password_file /mosquitto/auth/passwd
plugin_load  aclfile /usr/lib/mosquitto_acl_file.so
plugin_opt_acl_file /mosquitto/auth/acl

listener 1883
listener_allow_anonymous false
plugin_use pwfile
plugin_use aclfile
```

`plugin_load` declares, `plugin_use` binds to the enclosing listener, `global_plugin` binds to all
of them.

## M8 — `--test-config` is a syntax gate, not a correctness gate

```
mosquitto -c ok.conf  --test-config  ->  Configuration file is OK.        rc=0
mosquitto -c bad.conf --test-config  ->  Error: Unknown configuration variable 'this_is_garbage'.
                                         Error found at /tmp/bad.conf:2.  rc=3
```

Useful: a real exit code and a file:line. **But** it validates directive names only, and nothing a
plugin decides. Two configs that `--test-config` passed with `rc=0` and that then failed at
startup:

```
password-file: Error: Unknown option 'file'.                              # M7, bad plugin option
Error: `persistence true` cannot be used with a persistence plugin.       # persistence + persist-sqlite
```

So it catches typos in directives and not mistakes in plugin wiring. An initContainer running it
is worth having and is not a correctness gate.

## M9 — Dynsec bootstraps itself when its file is missing

```
Dynamic security plugin config not found, generating a default config.
  Generated passwords are at /tmp/ds.json.pw
```

A generated admin credential appearing on disk next to the config is a fact worth knowing about
before it appears in a pod.

## M12 — TLS material reloads on SIGHUP; a broken pair breaks the listener, not the process

*Measured 2026-10-05*, `eclipse-mosquitto:2.1.2-alpine`, docker 28.4.0, arm64. Listener 8883 with
`certfile`/`keyfile` on a bind mount; two certificates `CN=broker-a` and `CN=broker-b` from one
test CA. Certificate subject as served to a fresh `openssl s_client`:

```
--- served at start ---                 subject=CN=broker-a
--- files swapped, BEFORE sighup ---    subject=CN=broker-a
--- AFTER sighup ---                    subject=CN=broker-b
```

A subscriber connected over TLS **before** the reload stayed connected and received a message
published after it (`t/x after-reload`). So a renewed certificate reaches new connections on
SIGHUP, with no restart and no dropped clients.

The failure case — certificate `b` with key `a`, then SIGHUP:

```
OpenSSL Error [0]: error:05800074:x509 certificate routines::key values mismatch
Error when reloading certificate '/m/tls/tls.crt' or key '/m/tls/tls.key'.
OpenSSL Error while trying to get the error[0]: error:0A0000C1:SSL routines::no shared cipher
Client ::1 [::1:49176] disconnected: Protocol error.
```

The process keeps running and existing connections survive, but **the broker does not fall back
to the previous certificate: every new TLS handshake fails** until a valid pair arrives and a
second SIGHUP is sent — which recovered it (`subject=CN=broker-b`, the pre-existing subscriber
received `after-recovery`). Consequence: whatever sends the SIGHUP must check that certificate and
key match **before** signalling, or a half-edited Secret takes the listener down.

## M13 — An ACL of `#` grants nothing under `$`

*Measured 2026-10-05*, same rig, `password-file` + `acl-file` plugins, `listener_allow_anonymous
false`. User `wide` holds `topic readwrite #`; user `admin` holds explicit `$` grants.

| Probe | Result |
|---|---|
| `wide` subscribes `$SYS/broker/version` | `Timed out` — nothing delivered |
| `admin` (`topic read $SYS/#`) subscribes the same | `mosquitto version 2.1.2` |
| `wide` publishes `$custom/x`; `admin` (`topic readwrite $custom/#`) listens | not delivered; `admin`'s own publish to the same topic is |
| `wide` subscribes `$custom/#` explicitly; `admin` publishes | nothing delivered |
| `wide` round trip on `home/x` | delivered (control) |

So `#` in an ACL does not reach `$`-prefixed topics, in either direction; only an ACL entry that
itself starts with `$` does. A publish to `$CONTROL/probe` was not delivered even to a user with
an explicit `$CONTROL/#` grant, so that probe says nothing about ACLs and is not counted. Not
measured: shared subscriptions (`$share/<group>/<topic>`) and which topic their ACL check uses.

## M14 — A reload revokes: removed users and changed passwords are disconnected at once

*Measured 2026-10-05*, same rig, file plugins, `listener_allow_anonymous false`. User `victim`
held a subscription and a publishing connection; user `watcher` held a subscription throughout.

| Change, then SIGHUP | `victim`'s existing connections | Reconnect | `watcher` |
|---|---|---|---|
| `victim` removed from `passwd` and `acl` | `Client vsub ... disconnected.` / `Client vpub ... disconnected.` right after `Reloading config.` | `Connection Refused: not authorised` | untouched (0 disconnects), kept receiving |
| `victim`'s password changed | connection made with the old password disconnected | old password refused | untouched |
| `victim`'s ACL narrowed from `home/#` to `home/allowed/#` | **stays connected** | — | untouched |
| … and then a message to `home/forbidden` and one to `home/allowed/x` | only `home/allowed/x` delivered | — | both delivered |

So the broker re-checks every connected client's credentials on reload and drops exactly those
that no longer authenticate; ACLs are evaluated per message, so a narrowed ACL takes effect on a
live connection without dropping it. The opposite assumption — that an existing connection
survives a reload and keeps its rights until it reconnects — is what this repository believed
before the measurement, and it is false. Revocation latency is the time until the reload, i.e.
the kubelet's Secret propagation plus the signal, not "until the client reconnects".

## M15 — `spec.config` can open an anonymous listener; a global `allow_anonymous` cannot

*Measured 2026-10-05*, same rig. The generated file (listener 1883, `listener_allow_anonymous
false`, both plugins bound) with one appended block, anonymous `mosquitto_pub` against each port:

| Appended (as `spec.config` would) | Port 1883 | Port 1884 |
|---|---|---|
| `allow_anonymous true` | refused | no listener |
| `listener 1884` + `listener_allow_anonymous true` | refused | **ACCEPTED** |
| `listener 1884` alone | refused | refused |

A global `allow_anonymous true` does not override the listener-scoped `false`. A second listener
with `listener_allow_anonymous true` is a full bypass: it has no plugin bound, so its anonymous
clients are subject to no ACL, and messages cross listeners inside one broker — every user's
topics are readable and writable from it. It is not in the Service, but it is on the pod IP, and
nothing ships a NetworkPolicy. Side observation: with files at `mosquitto:root 0600` the broker
warns `group is not mosquitto. Future versions will refuse to load this file.` — the sidecar's
copy must land as `1883:1883`, which `runAsGroup: 1883` gives for free.

## M16 — Anonymous next to users: what `listener_allow_anonymous true` actually grants

*Measured 2026-10-05*, same rig, file plugins bound to the listener, `listener_allow_anonymous
true`, users `alice` (`readwrite home/#`) and a reference user `ref` (`readwrite #`).

| Probe | ACL without a general section | ACL with `topic read pub/#` before the first `user` line |
|---|---|---|
| anonymous client connects | yes (`New client connected ... as auto-...`, no username) | yes |
| anonymous read / write `home/x` | no / no | no / no |
| anonymous read / write `pub/x` | no / no | **yes** / no |
| `alice` read `pub/x` | no | **no** — the general section is not inherited by users |
| `alice` with a wrong password, no password; unknown user `ghost` | `Connection Refused: not authorised` | same |

So: anonymous access is deny-by-default under `acl-file`; the section before the first `user` line
is the anonymous ACL and applies to anonymous clients only; a client that presents a username is
never downgraded to anonymous when its credentials fail.

## M17 — Client-ID takeover crosses identities; `use_username_as_clientid` stops it and excludes anonymous

*Measured 2026-10-05*, same rig. `alice` holds a persistent session (`-c`, QoS 1, client ID
`z2m`), goes offline, `ref` publishes `queued-for-alice` to it.

Without countermeasure, `listener_allow_anonymous true`, anonymous ACL `readwrite home/#`:

| Probe | Result |
|---|---|
| anonymous connects with client ID `z2m`, clean session off | **receives `queued-for-alice`** — the queued message of `alice`'s session |
| `alice` reconnects as `z2m` | nothing — the message is gone |
| `alice` online as `z2m`; anonymous connects as `z2m` | `Client z2m ... disconnected: session taken over.`, then the two kick each other in a loop |
| **authenticated** `ref` connects as `z2m` | `alice` kicked the same way — **independent of anonymous access** |

With `use_username_as_clientid true` on the listener:

| Probe | Result |
|---|---|
| anonymous connects (any client ID) | `Connection Refused: not authorised` — **anonymous is refused outright although `listener_allow_anonymous true`** |
| `ref` connects with client ID `alice` | connected as `ref`; `alice`'s session untouched |
| `alice` reconnects with client ID `whatever` | receives `queued-for-alice` |
| `alice` holds two connections at once | they take each other over in a loop — one connection per username |

Consequences: any client that may connect can take over another client's session by guessing its
client ID — disconnecting it and receiving its queued messages, subject to its own ACL (anonymous
had `home/#` here; whether the ACL gates the stolen delivery otherwise is not measured).
`use_username_as_clientid` closes that across users, costs "one connection per username", and
cannot coexist with anonymous access on the same listener.

## M18 — The pinned image hashes `$7$` with 1000 iterations; argon2id does not work

*Measured 2026-10-05*, `eclipse-mosquitto:2.1.2-alpine`. `mosquitto_passwd`'s help says
`Defaults to argon2id, which is recommended.` What it does:

```
mosquitto_passwd -c -b p a pw                      -> a:$7$1000$<88 b64 salt>$<88 b64 hash>
mosquitto_passwd -H sha512-pbkdf2 -b p b pw        -> b:$7$1000$...
mosquitto_passwd -H argon2id -c -b p a pw          -> Error: Unable to hash password.  rc=10
```

The default silently falls back to PBKDF2-SHA512 (`$7$`) at **1000 iterations**. `libargon2.so.1`
is present in `/usr/lib`, but neither `mosquitto_passwd` nor `mosquitto_password_file.so` links it,
and the plugin's unresolved symbols include `PKCS5_PBKDF2_HMAC` and nothing from argon2. So the
broker can verify `$7$`; that it **cannot** verify `$argon2id$` is inferred from the symbols, not
login-tested. A statement that 2.1 "understands `$argon2id$`" does not hold for this build. Not measured: whether the plugin honours an iteration count other than 1000 written into
the `$7$<iterations>$` field.

## Not measured

- Anything on a real cluster. All of the above is single-container docker.
- Bridge behaviour, loop handling, retained-message propagation across a bridge.
- `persist-sqlite` under load or crash.
- Whether SIGHUP reload is atomic with respect to a half-written file. The design avoids the
  question rather than answering it: the file is written to a temporary name and renamed
  ([ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md)).
- Reload behaviour of the file plugins on the `arm64`/`amd64` split; measured on arm64 only.
