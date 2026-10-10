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
files on bind mounts. M1–M9 were measured on 2026-09-01, M12–M29 on 2026-10-05. **None of it
ran on a cluster**, except M22 and M23, which were measured on Kind, and where a section says it
was observed on Kind as well. The numbering has a gap: M10 and M11 measure a bridged broker pair and belong
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

*Added 2026-10-05:* it checks the **values** of the directives it knows as well, not only their
names. One line appended to the generated configuration at a time:

```
max_keepalive notanumber  ->  Error: 'max_keepalive' value not a number.           rc=3
max_qos 7                 ->  Error: 'max_qos' must be between 0 and 2 inclusive.
max_keepalive 70000       ->  Error: Invalid 'max_keepalive' value (70000).
log_type nonsense         ->  Error: Invalid 'log_type' value (nonsense).
retain_available maybe    ->  Error: Invalid 'retain_available' value (maybe).
autosave_interval -5      ->  Configuration file is OK.
```

each with `Error found at <file>:<line>.` So behind the `spec.config` allowlist, which refuses an
unknown directive before anything is written (M26), the `config-check` init container is where a
bad value of an allowed directive surfaces — not every bad value, as the last line shows.

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

## M19 — `--test-config` saves an empty database on exit, and reads no TLS file

*Measured 2026-10-05*, `eclipse-mosquitto:2.1.2-alpine`, run as `1883:1883`. A configuration with
`persistence true`, `persistence_location /mosquitto/data/` and one listener. A broker run first
stored one retained message (`mosquitto_pub -t keep/me -m retained-v1 -r -q 1`), leaving a
162-byte `mosquitto.db`. Then, on the same data directory:

```
mosquitto -c p.conf --test-config            (data mounted read-write)
  -> Configuration file is OK.
     mosquitto version 2.1.2 terminating
     Saving in-memory database to /mosquitto/data//mosquitto.db.        rc=0
  -> mosquitto.db is now 47 bytes
broker restarted on that directory, mosquitto_sub -t keep/me -C 1 -W 3
  -> Timed out                                                          rc=27

mosquitto -c p.conf --test-config            (data mounted read-only, read-only root fs)
  -> Configuration file is OK.
     ...
     Error saving in-memory database, unable to remove stale tmp file
     /mosquitto/data//mosquitto.db.new, error Read-only file system      rc=0
```

**`--test-config` does not load the database, and it saves its empty in-memory one on exit.** On
the real data volume that replaces every retained message and every persistent session with
nothing, on every pod start. So the `config-check` init container
([ADR 0007](../adr/0007-one-broker-image-pin-and-why-not-the-openssl-tag.md) D10) never mounts the
data volume: it sees a throwaway `emptyDir`, `config-check-scratch`, at the persistence path, where
the save succeeds and is discarded (`buildConfigCheckContainer` in
[`internal/builder/statefulset.go`](../../internal/builder/statefulset.go), asserted by
`TestBuildStatefulSet_ConfigCheckInitContainer`). A read-only mount would also protect the data but
prints an error line into every start's log.

The same run with `listener 8883`, `certfile /nonexistent/tls.crt` and `keyfile
/nonexistent/tls.key` prints `Configuration file is OK.` with `rc=0`: **`--test-config` opens no
TLS file**, so the init container needs no TLS mount. A misspelled directive fails with the line,
as M8 recorded: `Error: Unknown configuration variable 'max_queued_mesages'.`, `Error found at
/c/typo.conf:4.`, `rc=3` — and on Kind, from the init container of a broker pod, with the path of
the mounted file (`TestE2E_ConfigCheck_StopsATypoBeforeTheBroker`).

## M20 — A `$7$` hash rendered in Go is accepted, and has `mosquitto_passwd`'s exact shape

*Measured 2026-10-05*, `eclipse-mosquitto:2.1.2-alpine`. What `mosquitto_passwd -c -b p alice
s3cret` writes, taken apart:

```
alice:$7$1000$MvLfuap+p7Ti…6MjAEOE/8YJdvPPrG+Cm0vNu4RHg==$dqyaHMER…mbLYbhC/68qeA==
alg 7 · iterations 1000 · salt 88 chars = 64 bytes · key 88 chars = 64 bytes
PBKDF2-HMAC-SHA512(password, base64-decoded salt, 1000, 64) == base64-decoded key   -> True
```

So the format is `$7$<iterations>$<base64 salt>$<base64 key>`, standard base64 **with padding**,
the salt used as its decoded bytes. `auth.HashPassword` in
[`internal/auth/hash.go`](../../internal/auth/hash.go) writes exactly that from a 64-byte random
salt; a line it rendered, `probe:$7$1000$I2q6…$+Htf…`, loaded by the `password-file` plugin on
a listener with `listener_allow_anonymous false`:

```
mosquitto_pub -u probe -P go-rendered-pw   -> rc=0, … (p4, c1, k60, u'probe')
mosquitto_pub -u probe -P wrong            -> Connection Refused: not authorised   rc=5
```

`auth.VerifyPassword` accepts `mosquitto_passwd`'s own line for `s3cret` and nothing else
(`TestVerifyPassword_AcceptsTheBrokersOwnHash`); `TestHashPassword_HasTheShapeMosquittoPasswdWrites`
pins the shape `^\$7\$1000\$[A-Za-z0-9+/]{86}==\$[A-Za-z0-9+/]{86}==$`.

## M21 — Usernames with `@`, `.`, `_` and `-` work in both files

*Measured 2026-10-05*, same rig, both plugins, `listener_allow_anonymous false`. Users
`ha@home.lan` (`readwrite homeassistant/#`), `z2m.bridge` (`readwrite zigbee2mqtt/#`) and
`Zigbee_2MQTT-x` (`read zigbee2mqtt/#`), created with `mosquitto_passwd -b`:

| Probe | Result |
|---|---|
| `ha@home.lan` round trip on `homeassistant/x` | delivered |
| `z2m.bridge` round trip on `zigbee2mqtt/x` | delivered |
| `Zigbee_2MQTT-x` reads what `z2m.bridge` publishes | delivered |
| `ha@home.lan` subscribes `zigbee2mqtt/#`, `z2m.bridge` publishes | `Timed out` — not delivered |
| `z2m.bridge` listens, `ha@home.lan` publishes to `zigbee2mqtt/w` | publish `rc=0`, not delivered |
| `ha@home.lan` with a wrong password | `Connection Refused: not authorised`, `rc=5` |

Both parsers take every character of the username allowlist of
[ADR 0013](../adr/0013-a-client-is-a-mosquittouser-with-its-credentials-in-its-own-secret.md) D5
that is not alphanumeric, and the ACL is enforced per user under those names. A publish the ACL
denies is dropped silently: the client's `rc` is `0`.

## M22 — A sidecar as uid `1883` without capabilities signals the broker under `restricted`

*Measured 2026-10-05 on Kind* (`kindest/node:v1.36.1`), in a namespace labelled
`pod-security.kubernetes.io/enforce=restricted`. One pod, `shareProcessNamespace: true`, pod
`runAsUser: 1883`, every container `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem`,
`capabilities: drop: [ALL]`, seccomp `RuntimeDefault` — admitted. The broker container runs
`/usr/sbin/mosquitto`; a second container of the same image:

```
id                                   -> uid=1883(mosquitto) gid=1883(mosquitto)
grep ^Cap /proc/self/status          -> CapPrm/CapEff/CapBnd 0000000000000000
ls /proc                             -> 1 pause, 7 mosquitto (/usr/sbin/mosquitto -c …), …
kill -HUP 7                          -> rc=0;  broker log: Reloading config.
```

A third container with `runAsUser: 1000` and the same group: `kill -HUP 7` → `can't kill pid 7:
Operation not permitted`, `rc=1`. No container restarted. So the signal needs the same uid and no
capability, exactly as ADR 0014 D4 assumed; the broker is findable as the process whose
`/proc/<pid>/comm` is `mosquitto`, and the pod's PID 1 is the `pause` container. (`pgrep -x
mosquitto` from busybox returned nothing in a script that ran 8 s after start while `pgrep -l
mosquitto` found PID 7 seconds later; the reloader is Go and reads `/proc` itself.)

## M23 — The kubelet swaps a Secret volume in one step, about a minute after the change

*Measured 2026-10-05 on Kind* (`kindest/node:v1.36.1`, kubelet defaults). A pod mounting a Secret
with the keys `tls.crt` and `tls.key` printed both files and the target of `..data` whenever the
triple changed, polling every 0.2 s. The Secret was replaced three times with both keys changed:

| Update | Seen in the pod after |
|---|---|
| `v0` → `v1` | 75 s |
| `v1` → `v2` | 84 s |
| `v2` → `v3` | 69 s |

Each change appeared as one line with **both** new values and a new `..data` target
(`..2026_10_05_20_32_56.2733198498`, …) — never one file new and the other old. That is the
kubelet's atomic writer: the files are symlinks through `..data`, which is swapped in one
`rename`. A reader that reads the two files one after the other can still straddle a swap; reading
both through one resolved `..data` target cannot. The latency is the kubelet's sync period plus
its cache, not something the operator influences, and it is what revocation and a renewed
certificate wait for (ADR 0014 D8). Measured on one idle node; a loaded kubelet is not measured.

## M24 — A mismatched TLS pair at start stops the broker

*Measured 2026-10-05*, `eclipse-mosquitto:2.1.2-alpine`, two self-signed RSA-2048 pairs `A` and
`B` (`openssl req -x509 -newkey rsa:2048`). `listener 8883` with `certfile` from `A`:

```
keyfile from A  -> mosquitto version 2.1.2 running; mosquitto_pub over TLS rc=0
keyfile from B  -> Error: Unable to load server key file "/tls/keyB.pem". Check keyfile.
                   OpenSSL Error [0]: error:05800074:x509 certificate routines::key values mismatch
                   mosquitto version 2.1.2 terminating                      exit 1
```

So at start a mismatched pair is fatal — a pod would crash-loop — while on a reload the process
lives and the listener breaks (M12). `--test-config` does not open the files (M19), so the
`config-check` init container does not catch it either. Only a check of the pair before the
broker reads it — the reloader's, ADR 0001 D10, built as `checkTLS` in `internal/reloader` —
protects a running broker; a pod that starts with a bad pair fails visibly.

## M25 — A 2.0 image refuses the generated configuration at `--test-config`, with the line

*Measured 2026-10-05*, the configuration the user phase generates — persistence, both
`plugin_load` lines with their `plugin_opt_*`, `listener 1883`, `listener_allow_anonymous false`,
`use_username_as_clientid true`, both `plugin_use` — through `--test-config`:

```
eclipse-mosquitto:2.0.22        -> Error: Unknown configuration variable "plugin_load".
                                   Error found at /c/mosquitto.conf:4.                 rc=3
eclipse-mosquitto:2.1.2-alpine  -> Configuration file is OK.                          rc=0
```

So a `spec.image` on the 2.0 line stops in the `config-check` init container with the broker's
own message and the line, as ADR 0007 D10 expected. 2.0 quotes the directive with `"`, 2.1 with
`'`.

## M26 — The `spec.config` allowlist, from `mosquitto.conf(5)` of 2.1.2

*Taken 2026-10-05* from `man/mosquitto.conf.5.xml` at the upstream tag `v2.1.2`
(`gh api repos/eclipse-mosquitto/mosquitto/contents/man/mosquitto.conf.5.xml?ref=v2.1.2`). The
page documents 130 directives: 46 general, 21 listener, 16 listener TLS, 1 PSK, 33 bridge and 13
bridge TLS. `plugin_load` and `plugin_use` are not among them although the binary accepts them
(M7). The allowlist of [ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
D15 takes the tuning directives only — limits, queues, keepalive, persistence intervals, log
types:

```
autosave_interval  autosave_on_changes  connection_messages  global_max_clients
global_max_connections  log_timestamp  log_timestamp_format  log_type  max_connections
max_inflight_bytes  max_inflight_messages  max_keepalive  max_packet_size  max_qos
max_queued_bytes  max_queued_messages  max_topic_alias  max_topic_alias_broker  memory_limit
persistent_client_expiration  queue_qos0_messages  retain_available  retain_expiry_interval
set_tcp_nodelay  sys_interval  upgrade_outgoing_qos
```

Each of the 26, appended with a plausible value after the generated listener block, passes
`--test-config` of the pinned image with `Configuration file is OK.`. `max_connections`,
`max_qos`, `max_topic_alias` and `max_topic_alias_broker` are listener options and therefore
apply to the generated listener, because `spec.config` follows it. Left out on purpose:
`message_size_limit` (the image answers ``Note: It is recommended to replace
`message_size_limit` with `max_packet_size`.``), `allow_duplicate_messages` (deprecated), every
file and path directive (`persistence*`, `pid_file`, `log_dest`, `log_facility`, `include_dir`,
`http_dir`, `psk_file`, `user`), every listener, protocol, TLS and bridge directive, everything
that touches authentication or client identity (`*allow_anonymous`, `password_file`,
`acl_file`, `plugin*`, `global_plugin`, `per_listener_settings`, `use_*_as_*`,
`auth_plugin_deny_special_chars`, `allow_zero_length_clientid`, `auto_id_prefix`,
`clientid_prefixes`, `check_retain_source`, `enable_control_api`), and `mount_point`, which
rewrites every topic of the listener under the ACLs.

## M27 — An ACL topic may contain spaces; the rest of the line is the topic

*Measured 2026-10-05*, same rig, both plugins. A user with the single entry
`topic readwrite home/living room/#`:

```
subscribe "home/living room/#", publish "home/living room/lamp"  -> delivered
subscribe "home/living", publish "home/living"                   -> Timed out
```

The `acl-file` parser takes everything after the access word as the topic, spaces included, and
does not stop at the first space. So the renderer refuses only what changes the meaning of a line —
a line break, any other control character, leading or trailing whitespace — and lets a topic with an
inner space through (`auth.topicProblem` in [`internal/auth/render.go`](../../internal/auth/render.go)).

## M28 — The `$SYS` topics the exporter maps, and what they carry

*Measured 2026-10-05*, `eclipse-mosquitto:2.1.2-alpine`, docker 28.4.0, arm64, the three-line
configuration of [ADR 0002](../adr/0002-the-metrics-exporter-is-written-here.md) (`log_dest
stdout`, `listener 1883`, `allow_anonymous true`), `mosquitto_sub -v -t '$SYS/#' -W 22` from the
same container. 55 distinct topics, the same count ADR 0002 recorded; one name carries a space,
`$SYS/broker/retained messages/count`. Payloads are decimal integers, except:

```
$SYS/broker/load/bytes/received/1min 19.35          # every load/… topic: a decimal fraction
$SYS/broker/uptime 21 seconds                       # a number and a unit
$SYS/broker/version mosquitto version 2.1.2         # a string
```

The 24 `load/…` topics are `{bytes,messages,publish}/{received,sent}`, `publish/dropped`,
`connections` and `sockets`, each over `1min`, `5min` and `15min`. Every one of the 55 is
retained: a subscriber started 12 seconds after the broker received all 55 within one second, and
`mosquitto_sub --retained-only` the same 55. A new subscription therefore sees every value at once,
without waiting for `sys_interval`. The exporter's mapping table
is this set ([`internal/exporter/mapping.go`](../../internal/exporter/mapping.go)), plus
`clients/maximum`, which ADR 0002 measured on 2.0.22 only.

What it sees at once are the values of the broker's last publish, from before its own login.
*Measured 2026-10-10*, same image, docker 28.4.0, arm64, `sys_interval 10`, the only client a
`mosquitto_sub -v -t '$SYS/broker/clients/connected' -t '$SYS/broker/uptime'` started 13 seconds
after the broker:

```
11:36:00 $SYS/broker/clients/connected 0     # retained, on subscribe: the subscriber is not counted
11:36:00 $SYS/broker/uptime 13 seconds
11:36:10 $SYS/broker/uptime 23 seconds
11:36:10 $SYS/broker/clients/connected 1     # the next publish counts it
```

So the exporter counts in its own `mosquitto_clients_connected` only up to `sys_interval` after
it logged in. The E2E metrics test waits for that series rather than reading it from the first
scrape that shows `mosquitto_exporter_connected 1`, which failed twice in CI on 2026-10-07 and
2026-10-10 with `mosquitto_clients_connected 0`.

## M29 — `max_packet_size` defaults to 2,000,000 bytes, and a later line wins

*Measured 2026-10-05*, same rig. With no `max_packet_size` line, a QoS 0 publish of a 1,999,000-byte
payload is accepted and one of 2,000,100 bytes is refused — the broker logs `disconnected: oversize
packet`, while `mosquitto_pub` still exits `0`. A file with `sys_interval 10` and
`max_packet_size 2000000` early and `sys_interval 2` and `max_packet_size 3100000` after the
listener: `$SYS/broker/uptime` arrives every 2 seconds and a 3,000,000-byte payload is accepted.
So the generated file can state both defaults (ADR 0002 D7), and a `spec.config` line, appended
after it, still overrides them.

## Not measured

- Anything on a production cluster. M22 and M23 ran on Kind; the rest is single-container docker.
- Bridge behaviour, loop handling, retained-message propagation across a bridge.
- `persist-sqlite` under load or crash.
- Whether SIGHUP reload is atomic with respect to a half-written file. The design avoids the
  question rather than answering it: the file is written to a temporary name and renamed
  ([ADR 0014](../adr/0014-credentials-reach-the-broker-as-one-rendered-secret-and-a-signal-never-as-a-restart.md)).
- Reload behaviour of the file plugins on the `arm64`/`amd64` split; measured on arm64 only.
