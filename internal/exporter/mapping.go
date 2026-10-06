package exporter

import (
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// sysPrefix is the root of every topic the exporter maps.
const sysPrefix = "$SYS/broker/"

// metric is how one $SYS topic becomes a Prometheus series.
type metric struct {
	name string
	help string
	kind prometheus.ValueType
}

// table maps the $SYS topics of the pinned image (M28), plus clients/maximum,
// which 2.0 publishes (ADR 0002). A topic that is not in the table, and not a
// load average, is skipped: a broker version that publishes more is not a
// reason to invent a series for it. A topic in the table that the broker does
// not publish is absent from the scrape, never zero.
var table = map[string]metric{
	"bytes/received":             {"mosquitto_bytes_received_total", "Bytes received since the broker started.", prometheus.CounterValue},
	"bytes/sent":                 {"mosquitto_bytes_sent_total", "Bytes sent since the broker started.", prometheus.CounterValue},
	"clients/active":             {"mosquitto_clients_active", "Connected clients.", prometheus.GaugeValue},
	"clients/connected":          {"mosquitto_clients_connected", "Connected clients.", prometheus.GaugeValue},
	"clients/disconnected":       {"mosquitto_clients_disconnected", "Persistent clients that are disconnected.", prometheus.GaugeValue},
	"clients/expired":            {"mosquitto_clients_expired", "Persistent clients expired and removed.", prometheus.GaugeValue},
	"clients/inactive":           {"mosquitto_clients_inactive", "Persistent clients that are disconnected.", prometheus.GaugeValue},
	"clients/maximum":            {"mosquitto_clients_maximum", "The most clients connected at once (Mosquitto 2.0).", prometheus.GaugeValue},
	"clients/total":              {"mosquitto_clients_total", "Connected and disconnected persistent clients.", prometheus.GaugeValue},
	"connections/socket/count":   {"mosquitto_connections_socket_count", "Open sockets.", prometheus.GaugeValue},
	"heap/current":               {"mosquitto_heap_current_bytes", "Heap memory in use.", prometheus.GaugeValue},
	"heap/maximum":               {"mosquitto_heap_maximum_bytes", "The most heap memory in use since the broker started.", prometheus.GaugeValue},
	"messages/received":          {"mosquitto_messages_received_total", "MQTT packets of any type received since the broker started.", prometheus.CounterValue},
	"messages/sent":              {"mosquitto_messages_sent_total", "MQTT packets of any type sent since the broker started.", prometheus.CounterValue},
	"messages/stored":            {"mosquitto_messages_stored", "Messages held in the message store.", prometheus.GaugeValue},
	"packet/out/bytes":           {"mosquitto_packet_out_bytes", "Bytes of outgoing packets queued.", prometheus.GaugeValue},
	"packet/out/count":           {"mosquitto_packet_out_count", "Outgoing packets queued.", prometheus.GaugeValue},
	"publish/bytes/received":     {"mosquitto_publish_bytes_received_total", "PUBLISH payload bytes received since the broker started.", prometheus.CounterValue},
	"publish/bytes/sent":         {"mosquitto_publish_bytes_sent_total", "PUBLISH payload bytes sent since the broker started.", prometheus.CounterValue},
	"publish/messages/dropped":   {"mosquitto_publish_messages_dropped_total", "PUBLISH messages dropped since the broker started.", prometheus.CounterValue},
	"publish/messages/received":  {"mosquitto_publish_messages_received_total", "PUBLISH messages received since the broker started.", prometheus.CounterValue},
	"publish/messages/sent":      {"mosquitto_publish_messages_sent_total", "PUBLISH messages sent since the broker started.", prometheus.CounterValue},
	"retained messages/count":    {"mosquitto_retained_messages", "Retained messages.", prometheus.GaugeValue},
	"shared_subscriptions/count": {"mosquitto_shared_subscriptions", "Shared subscriptions.", prometheus.GaugeValue},
	"store/messages/bytes":       {"mosquitto_store_messages_bytes", "Payload bytes held in the message store.", prometheus.GaugeValue},
	"store/messages/count":       {"mosquitto_store_messages", "Messages held in the message store.", prometheus.GaugeValue},
	"subscriptions/count":        {"mosquitto_subscriptions", "Subscriptions.", prometheus.GaugeValue},
	"uptime":                     {"mosquitto_uptime_seconds", "Seconds since the broker started.", prometheus.GaugeValue},
}

// loadWindows are the averaging windows of the load/... topics.
var loadWindows = map[string]bool{"1min": true, "5min": true, "15min": true}

// sample is one series value the exporter holds.
type sample struct {
	metric metric
	label  string // the window of a load average, or the version string
	value  float64
}

// The two series that are not a plain number.
const (
	versionMetricName = "mosquitto_version_info"
	loadMetricPrefix  = "mosquitto_load_"
)

// parse turns one $SYS message into the sample it sets, or reports false for a
// topic the exporter does not map or a payload it cannot read.
func parse(topic string, payload []byte) (sample, bool) {
	path, ok := strings.CutPrefix(topic, sysPrefix)
	if !ok {
		return sample{}, false
	}
	text := strings.TrimSpace(string(payload))

	if path == "version" {
		version := strings.TrimPrefix(text, "mosquitto version ")
		return sample{
			metric: metric{versionMetricName, "The broker version, as the label version.", prometheus.GaugeValue},
			label:  version,
			value:  1,
		}, version != ""
	}

	if what, ok := strings.CutPrefix(path, "load/"); ok {
		i := strings.LastIndex(what, "/")
		if i <= 0 || !loadWindows[what[i+1:]] {
			return sample{}, false
		}
		value, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return sample{}, false
		}
		name := loadMetricPrefix + strings.ReplaceAll(what[:i], "/", "_")
		return sample{
			metric: metric{name, "Moving average per minute over the window in the label window.", prometheus.GaugeValue},
			label:  what[i+1:],
			value:  value,
		}, true
	}

	m, known := table[path]
	if !known {
		return sample{}, false
	}
	if path == "uptime" {
		text = strings.TrimSuffix(text, " seconds")
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return sample{}, false
	}
	return sample{metric: m, value: value}, true
}
