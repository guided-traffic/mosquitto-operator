package exporter

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Collector holds the last value of every mapped $SYS topic and serves them to
// a scrape. It is an unchecked collector: the set of series follows what the
// broker publishes, so it is not known when the collector is registered.
type Collector struct {
	mu        sync.Mutex
	samples   map[string]sample
	connected bool
}

// connectedDesc is the exporter's own series: whether it holds an MQTT session
// with its broker right now.
var connectedDesc = prometheus.NewDesc("mosquitto_exporter_connected",
	"Whether the exporter holds an MQTT session with its broker (1) or not (0). Without one, no broker series is served.",
	nil, nil)

// NewCollector returns a collector that serves nothing but
// mosquitto_exporter_connected 0 until it is connected.
func NewCollector() *Collector {
	return &Collector{samples: map[string]sample{}}
}

// Observe records one $SYS message. It reports whether the topic was mapped.
func (c *Collector) Observe(topic string, payload []byte) bool {
	s, ok := parse(topic, payload)
	if !ok {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := s.metric.name
	if s.metric.name != versionMetricName {
		key += "/" + s.label
	}
	c.samples[key] = s
	return true
}

// Connected records that a session was established. The values of the session
// before stay dropped: every $SYS topic is retained, so a new subscription
// receives all of them at once (M28).
func (c *Collector) Connected() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connected = true
}

// Lost drops every value, so a scrape while the broker is unreachable shows the
// series absent rather than stale (ADR 0002: a missing value is never zero and
// never the last one seen).
func (c *Collector) Lost() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connected = false
	c.samples = map[string]sample{}
}

// Describe sends nothing, which makes this an unchecked collector.
func (c *Collector) Describe(chan<- *prometheus.Desc) {}

// Collect sends the connection state and every value held.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()
	connected := 0.0
	if c.connected {
		connected = 1
	}
	ch <- prometheus.MustNewConstMetric(connectedDesc, prometheus.GaugeValue, connected)
	for _, s := range c.samples {
		ch <- s.constMetric()
	}
}

// constMetric is the sample as a Prometheus series.
func (s sample) constMetric() prometheus.Metric {
	switch {
	case s.metric.name == versionMetricName:
		desc := prometheus.NewDesc(s.metric.name, s.metric.help, []string{"version"}, nil)
		return prometheus.MustNewConstMetric(desc, s.metric.kind, s.value, s.label)
	case s.label != "":
		desc := prometheus.NewDesc(s.metric.name, s.metric.help, []string{"window"}, nil)
		return prometheus.MustNewConstMetric(desc, s.metric.kind, s.value, s.label)
	default:
		desc := prometheus.NewDesc(s.metric.name, s.metric.help, nil, nil)
		return prometheus.MustNewConstMetric(desc, s.metric.kind, s.value)
	}
}
