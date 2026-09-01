# mosquitto-operator

[![Build Status](https://github.com/guided-traffic/mosquitto-operator/actions/workflows/release.yml/badge.svg)](https://github.com/guided-traffic/mosquitto-operator/actions)
[![Coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/guided-traffic/mosquitto-operator/main/.github/badges/coverage.json)](https://github.com/guided-traffic/mosquitto-operator)
[![Go Report Card](https://goreportcard.com/badge/github.com/guided-traffic/mosquitto-operator)](https://goreportcard.com/report/github.com/guided-traffic/mosquitto-operator)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

A Kubernetes operator that provisions Eclipse Mosquitto MQTT brokers from a
`Mosquitto` custom resource. One resource produces a StatefulSet of broker pods,
a ConfigMap holding the generated `mosquitto.conf`, a headless Service giving
each pod a DNS name and a ClusterIP Service in front of all of them - with
optional per-pod persistence, an optional anti-affinity spread over nodes and an
optional TLS listener.

## What this version is

The broker pods are independent Mosquitto processes behind one Service. There is
no bridging between them, no shared session state and no shared retained
messages, and no clustering: a subscriber on one pod does not receive a retained
message that was published through another, and a client that reconnects onto a
different pod starts from an empty session. Raising `spec.replicas` therefore
buys process redundancy, not a highly available broker.

Highly available brokers are the goal of this project; this version does not
deliver them yet.

### Anonymous access

The generated configuration accepts anonymous clients. Mosquitto 2.x rejects
every client on a listener that has no configured authentication, and this API
models none, so the generated `mosquitto.conf` sets `allow_anonymous true` -
without it the default resource would serve nobody. Anything that can route to
the broker's ClusterIP Service can publish and subscribe.

Until the API models authentication, `spec.config` is where it goes: its content
is appended to the generated file verbatim, and a repeated global option
overrides the generated one. On the pinned image
(`eclipse-mosquitto:2.1.2-alpine`) that means the `password-file` and `acl-file`
plugins - their `password_file` and `acl_file` predecessors are deprecated in
Mosquitto 2.1 and removed in 3.0.

### TLS

`spec.tls.secretName` names an existing Secret in the resource's own namespace
carrying `tls.crt` and `tls.key`. The operator mounts it into the broker pods and
moves the listener to MQTTS on port 8883 - in the generated block that replaces
the plaintext listener rather than adding to it, though a `spec.config` of your
own can declare further listeners the operator neither models nor exposes as a
container or Service port. TLS encrypts the connection and proves the broker's
identity to its clients; it authenticates no client to the broker, because the
generated configuration sets no `require_certificate`.

The operator neither creates nor renews that Secret. Both ways of filling it are
supported and neither needs anything from this project:

1. by hand: `kubectl create secret tls <name> --cert=... --key=...`
2. a cert-manager `Certificate`, on clusters that already run cert-manager. The
   administrator owns that object; this project has no cert-manager dependency
   and installs nothing.

The operator does not watch the Secret. Renewing or replacing the certificate
changes the Secret, but running broker pods keep serving the material they
started with, so a rotation only takes effect once the pods restart (for example
`kubectl rollout restart statefulset/<name>`).
