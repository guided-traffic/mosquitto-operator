package exporter

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	tests := []struct {
		topic, payload string
		name           string
		label          string
		value          float64
		kind           prometheus.ValueType
	}{
		{"$SYS/broker/bytes/received", "27", "mosquitto_bytes_received_total", "", 27, prometheus.CounterValue},
		{"$SYS/broker/clients/connected", "1", "mosquitto_clients_connected", "", 1, prometheus.GaugeValue},
		{"$SYS/broker/retained messages/count", "55", "mosquitto_retained_messages", "", 55, prometheus.GaugeValue},
		{"$SYS/broker/heap/current", "838936", "mosquitto_heap_current_bytes", "", 838936, prometheus.GaugeValue},
		{"$SYS/broker/uptime", "21 seconds", "mosquitto_uptime_seconds", "", 21, prometheus.GaugeValue},
		{"$SYS/broker/load/bytes/received/1min", "19.35", "mosquitto_load_bytes_received", "1min", 19.35, prometheus.GaugeValue},
		{"$SYS/broker/load/publish/dropped/15min", "0.00", "mosquitto_load_publish_dropped", "15min", 0, prometheus.GaugeValue},
		{"$SYS/broker/version", "mosquitto version 2.1.2", "mosquitto_version_info", "2.1.2", 1, prometheus.GaugeValue},
	}
	for _, tt := range tests {
		t.Run(tt.topic, func(t *testing.T) {
			s, ok := parse(tt.topic, []byte(tt.payload))
			require.True(t, ok)
			assert.Equal(t, tt.name, s.metric.name)
			assert.Equal(t, tt.label, s.label)
			assert.InDelta(t, tt.value, s.value, 1e-9)
			assert.Equal(t, tt.kind, s.metric.kind)
		})
	}
}

// TestParse_SkipsWhatItCannotMap: a topic outside the table, a load window
// nobody publishes and a payload that is not a number are skipped - never
// turned into a zero (ADR 0002).
func TestParse_SkipsWhatItCannotMap(t *testing.T) {
	for topic, payload := range map[string]string{
		"$SYS/broker/something/new":        "5",
		"$SYS/broker/load/bytes/sent/2min": "1.0",
		"$SYS/broker/load/1min":            "1.0",
		"$SYS/broker/clients/connected":    "many",
		"$SYS/broker/version":              "",
		"home/temperature":                 "21",
	} {
		_, ok := parse(topic, []byte(payload))
		assert.False(t, ok, "%s %q", topic, payload)
	}
}

// TestTable_CoversTheMeasuredTopics: every numeric topic of M28 is mapped.
func TestTable_CoversTheMeasuredTopics(t *testing.T) {
	measured := []string{
		"bytes/received", "bytes/sent", "clients/active", "clients/connected", "clients/disconnected",
		"clients/expired", "clients/inactive", "clients/total", "connections/socket/count", "heap/current",
		"heap/maximum", "messages/received", "messages/sent", "messages/stored", "packet/out/bytes",
		"packet/out/count", "publish/bytes/received", "publish/bytes/sent", "publish/messages/dropped",
		"publish/messages/received", "publish/messages/sent", "retained messages/count",
		"shared_subscriptions/count", "store/messages/bytes", "store/messages/count", "subscriptions/count",
		"uptime",
	}
	for _, path := range measured {
		_, ok := table[path]
		assert.True(t, ok, path)
	}
	names := map[string]string{}
	for path, m := range table {
		if other, taken := names[m.name]; taken {
			t.Errorf("%s and %s both map to %s", path, other, m.name)
		}
		names[m.name] = path
		if m.kind == prometheus.CounterValue {
			assert.True(t, strings.HasSuffix(m.name, "_total"), "a counter is named *_total: %s", m.name)
		}
	}
}

// lines splits a scrape into its lines, so an assertion matches a whole sample
// line and never a HELP text that happens to contain it.
func lines(body string) []string {
	return strings.Split(body, "\n")
}

func scrape(t *testing.T, c *Collector) string {
	t.Helper()
	server := httptest.NewServer(Handler(c))
	defer server.Close()
	resp, err := http.Get(server.URL + "/metrics")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

// TestCollector_ServesWhatItHoldsAndDropsItOnALostSession: values appear as
// series; a lost session drops them all, so a scrape shows them absent and
// the connection gauge at 0 - never a stale value.
func TestCollector_ServesWhatItHoldsAndDropsItOnALostSession(t *testing.T) {
	c := NewCollector()
	assert.Contains(t, lines(scrape(t, c)), "mosquitto_exporter_connected 0")

	c.Connected()
	assert.True(t, c.Observe("$SYS/broker/clients/connected", []byte("3")))
	assert.True(t, c.Observe("$SYS/broker/load/sockets/1min", []byte("0.72")))
	assert.True(t, c.Observe("$SYS/broker/load/sockets/5min", []byte("0.20")))
	assert.True(t, c.Observe("$SYS/broker/version", []byte("mosquitto version 2.1.2")))
	assert.True(t, c.Observe("$SYS/broker/bytes/sent", []byte("5462")))
	assert.False(t, c.Observe("$SYS/broker/unknown", []byte("1")))

	body := lines(scrape(t, c))
	for _, line := range []string{
		"mosquitto_exporter_connected 1",
		"mosquitto_clients_connected 3",
		`mosquitto_load_sockets{window="1min"} 0.72`,
		`mosquitto_load_sockets{window="5min"} 0.2`,
		`mosquitto_version_info{version="2.1.2"} 1`,
		"# TYPE mosquitto_bytes_sent_total counter",
		"mosquitto_bytes_sent_total 5462",
	} {
		assert.Contains(t, body, line)
	}

	c.Lost()
	body = lines(scrape(t, c))
	assert.Contains(t, body, "mosquitto_exporter_connected 0")
	assert.NotContains(t, strings.Join(body, "\n"), "mosquitto_clients_connected", "a lost session leaves no stale value")
}

func TestMain_RefusesUnknownFlags(t *testing.T) {
	var stderr bytes.Buffer
	assert.Equal(t, 2, Main([]string{"--no-such-flag"}, &stderr))
	assert.Equal(t, 1, Main([]string{"--listen", "256.0.0.1:1"}, &stderr))
}
