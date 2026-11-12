# Moving clients from an existing broker

How to move the clients of a broker you already run onto a broker of this operator, one client at a
time, and what to expect on the way. Everything here was done and observed on 2026-11-12, when a
Home Assistant installation moved off an Ansible-installed Helm broker on a production cluster
(Kubernetes v1.37.1, Cilium, Rook Ceph RBD; the operator from the published chart `0.2.18`/`0.2.19`
through Flux). The broker had about thirty clients: Home Assistant, an automation engine that keeps
its own state in retained topics, two fleets of Wi-Fi devices (presence sensors and smart plugs), a
heating-bus bridge speaking MQTT 3.1 and two solar pollers that reached the broker through a bridge.
What was not observed is said where it matters.

## The shape

- **Run the new broker beside the old one**, under a name of its own and with a LAN address of its
  own. The operator refuses to write onto an object it does not own
  ([ADR 0009](../adr/0009-delete-only-through-owner-references.md)), so it cannot take over the old
  broker's Service, and a parallel broker can be checked before any client depends on it.
- **One `MosquittoUser` per client — per device, too.** A generated listener allows one connection
  per username ([ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
  D14): a fleet that shares one login on the old broker would make its devices take each other's
  session in a loop. Since every client gets new credentials anyway, a user of its own costs nothing
  extra and makes one device revocable alone. Two pollers that publish at the same moments need two
  users for the same reason.
- **Derive the ACLs from the traffic, not from the old ACL file.** The old broker of this migration
  did not enforce its ACLs at all: the chart appended `acl_file` after the last `listener`, and
  under `per_listener_settings true` that bound it to a listener no client used. Its clients had
  long been publishing outside their ACLs — status topics, discovery messages, and heating commands
  from a user that was meant to read only. Copied 1:1, those ACLs would have dropped those
  publishes silently. Read the topics from the live traffic and the retained store of the old broker
  first.
- **A LAN address is yours to add.** The operator renders no LoadBalancer
  ([ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
  D16). Select `app.kubernetes.io/instance=<name>` **and**
  `app.kubernetes.io/managed-by=mosquitto-operator`: a Helm-installed broker may carry the same
  `name` and `instance` labels, and must never become an endpoint of the new Service. Check the
  firewall between the device networks and the new address before the first device moves.
- **A NetworkPolicy for the in-cluster clients**, as the [fast start](../../README.md#-tldr-fast-start)
  shows, if the cluster denies by default.

## Keeping both brokers in sync while clients move

Home Assistant talks to one broker. Without a link between the two, every device that moves
disappears from it until it moves too. The link used here was a **relay**: a plain Mosquitto
outside this operator, with no clients of its own and two bridges, one to each broker.

```conf
persistence false
listener 1883 127.0.0.1
allow_anonymous false

connection old-broker
address <old broker>:1883
remote_username <a user of the old broker>
remote_password <its password>
try_private true
cleansession true
topic # both 1

connection new-broker
address <name>.<ns>.svc:1883
remote_username migration-bridge          # a MosquittoUser with readwrite #
remote_password <its password>
try_private true
cleansession true
topic # both 1
```

Measured with `eclipse-mosquitto:2.0.22-openssl` as the old and `2.1.2-alpine` as the new broker and
the relay, configured as the operator renders its listener: a retained value on the old broker
arrived on the new one when the relay connected; a live message from either side arrived **exactly
once** on both; a retained value published on the new side arrived retained on the old one. On the
cluster the relay copied about 1,400 retained topics in its first seconds and then carried about 33
messages a second.

Two things the relay does that you have to plan for:

- **A restarted broker replays its persisted retained values to the other side.** Measured: the new
  broker stopped with `x = v1` on disk, the old one moved to `v2`, the new one started again —
  afterwards the old broker held `v1`. Whatever changed during a restart can be reverted on the side
  that did not restart. Every operator release restarts every broker once, because the reloader
  runs the operator's image. **Move the readers first** — Home Assistant and the automation engine —
  so that a restart of the new broker can only stale the old one, which nobody reads anymore.
- **The old broker's will can overtake a moved device.** A device that reboots without sending
  DISCONNECT is back on the new broker within seconds; the old broker fires its retained `offline`
  only after the keepalive timeout, and the relay carries that stale `offline` over. Home Assistant
  then marks a device unavailable that is online. Restarting the device once more, after its old
  session is gone, sets the true status. Devices that disconnect cleanly before they restart did not
  show it.

The relay holds `readwrite #` on both brokers. Switch it off when the last client has moved, then
retire the old broker.

## Moving one client

1. Give the client its new address, username and password — on the device, or in the workload's
   configuration. A user created after the broker started is refused for about **90 seconds**: the
   kubelet refreshes the mounted credentials, then the reloader reloads the broker
   ([users.md](users.md#how-long-a-change-takes)). The clients retried and got in; expect `not
   authorised` in that window.
2. Check its login in the broker log: `kubectl logs <name>-0 -c mosquitto` shows `New client
   connected … u'<username>'`. The client ID is the username on this broker
   ([ADR 0008](../adr/0008-the-generated-broker-is-anonymous-and-spec-config-can-undo-the-rest.md)
   D14); a client that keeps using its own ID in topics, such as a birth and will topic, is not
   affected.
3. An application that restores its state from its own retained topics found them on the new
   broker, copied there by the relay, and restarted where it had left off.

Rolling a client back means giving it its old address and credentials again; the old broker is
untouched until it is retired.

## A password rotated in Git

Observed on 2026-11-12 with one solar poller, whose credentials Secret is mounted as a directory
and read again on every publish:

| | Time (UTC) |
|---|---|
| new SOPS-encrypted Secret pushed | 15:45:37 |
| Flux applied it | 15:46:16 |
| the reloader copied the new credentials and reloaded the broker | 15:46:52 |

The poller published every 16 seconds throughout, and **no login was refused**. After the reload the
broker knows only the new password, so the successful logins after it are the new one. Broker and
poller ran on the same node, whose kubelet refreshed both mounts in one pass; on different nodes the
two refresh independently, and the client can be refused for up to one kubelet sync period until
both sides agree. A client that reads its password from an environment variable needs a restart
([users.md](users.md)).

## What this page does not cover

- TLS: no client of this migration used it, so `spec.tls` was not part of it.
- More than one replica, the kustomize install path, and the relay running across more than one
  operator release.
- A rotation with the broker and the client on different nodes.
