# Broker metrics

How to get a broker's statistics into Prometheus, what the series mean, and what the exporter
costs. The field, the names and the full list of series are in the
[README reference](../../README.md#specmetrics-the-broker-exporter); the decision is
[ADR 0002](../adr/0002-the-metrics-exporter-is-written-here.md).

## Turning it on

```yaml
apiVersion: mko.gtrfc.com/v1
kind: Mosquitto
metadata:
  name: broker
spec:
  metrics:
    enabled: true
```

The pods roll once: the `exporter` container is part of the pod template. The operator also
renders the user `mko-exporter` into `<name>-auth` and keeps its generated password there under
the key `exporter-password`; that change reaches the running broker like any user change, so the
exporter of a freshly rolled pod logs in at once. Turning it off rolls the pods again and removes
the user.

Observed on Kind (`TestE2E_Metrics_TheExporterServesTheBrokersSysTree`, 2026-10-05): a broker
with a plain listener and one under `spec.tls` each served `mosquitto_exporter_connected 1` and the
broker series within seconds of becoming ready, and a `MosquittoUser` whose Secret names the
username `mko-exporter` reported `UsernameReserved` while the exporter stayed connected.

## Finding the pods

Each broker pod serves its own `/metrics` on its pod IP, port `9234`, port name `metrics`. No
Service exposes it and the operator ships no ServiceMonitor or PodMonitor, so the scraper finds the
pods itself. With the Prometheus Operator, a `PodMonitor` of your own:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata:
  name: mosquitto
  namespace: home                                   # example — the brokers' namespace
spec:
  selector:
    matchLabels:
      app.kubernetes.io/managed-by: mosquitto-operator
  podMetricsEndpoints:
    - port: metrics
      path: /metrics
      interval: 30s                                 # example — sys_interval is 10 s
```

Not run here: no test installs the Prometheus Operator. A plain Prometheus does the same with
`kubernetes_sd_configs` of role `pod`, keeping the targets whose container port is named
`metrics` and whose pod carries `app.kubernetes.io/managed-by=mosquitto-operator`.

**A NetworkPolicy has to let the scraper in.** The example in the
[README fast start](../../README.md#-tldr-fast-start) admits MQTT clients on `1883` and nothing
else; add an ingress rule for port `9234` from the scraper's namespace.

## What the series mean

- **One broker per series.** `$SYS` describes one Mosquitto process, and the pods of a
  `Mosquitto` are independent brokers with no shared state. Do not sum a gauge across pods:
  `mosquitto_clients_connected` of two pods is two brokers' clients, not one broker's.
- **Absent, not zero.** While the exporter has no MQTT session — the broker restarting, the
  password not yet loaded — it serves `mosquitto_exporter_connected 0` and no broker series at
  all. A topic the broker does not publish has no series. Alert on
  `mosquitto_exporter_connected == 0`, and on the series being absent.
- **Counters restart with the broker.** The `_total` series count since the broker process
  started; a restarted pod starts them again from zero, which `rate()` treats as a reset.
- **Ten-second resolution.** The broker publishes `$SYS` every `sys_interval`, which the generated
  file states as `10`; a scrape between two updates sees the same values.
- **Every `$SYS` topic is retained** (M28), so an exporter that reconnects has every value back at
  once.

## What it costs

- A container per broker pod with the fixed resources of `reloader`: requests `10m` CPU and `32Mi`
  memory, a `64Mi` memory limit. It is not configurable.
- One MQTT connection per broker, as `mko-exporter`, which counts in the broker's own
  `clients/connected`.
- A plaintext password in `<name>-auth`: the operator's own, for a user that can read `$SYS` and
  nothing else ([credentials.md](../security/credentials.md#the-exporters-password)).
- **An unauthenticated endpoint.** Whatever reaches the pod IP on `9234` reads the broker's
  statistics — client counts, traffic, the broker version. It carries no message payload, no
  topic other than the `$SYS` names, no client ID and no credential
  ([trust-boundaries.md H-20](../security/trust-boundaries.md#h-20)).
