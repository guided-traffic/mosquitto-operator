//go:build imagetools

package imagetools

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/builder"
	"github.com/guided-traffic/mosquitto-operator/test/testimages"
)

// allowlistSamples holds one plausible value per allowed spec.config directive.
// It must cover builder.AllowedConfigDirectives exactly, so a directive added
// to the allowlist arrives with a value the pinned image is asked about
// (docs/developer/broker-behaviour.md M26).
var allowlistSamples = map[string]string{
	"autosave_interval":            "1800",
	"autosave_on_changes":          "false",
	"connection_messages":          "true",
	"global_max_clients":           "1000",
	"global_max_connections":       "1000",
	"log_timestamp":                "true",
	"log_timestamp_format":         "%Y-%m-%dT%H:%M:%S",
	"log_type":                     "debug",
	"max_connections":              "500",
	"max_inflight_bytes":           "0",
	"max_inflight_messages":        "20",
	"max_keepalive":                "120",
	"max_packet_size":              "1048576",
	"max_qos":                      "1",
	"max_queued_bytes":             "0",
	"max_queued_messages":          "1000",
	"max_topic_alias":              "10",
	"max_topic_alias_broker":       "10",
	"memory_limit":                 "0",
	"persistent_client_expiration": "14d",
	"queue_qos0_messages":          "false",
	"retain_available":             "true",
	"retain_expiry_interval":       "3600",
	"set_tcp_nodelay":              "true",
	"sys_interval":                 "10",
	"upgrade_outgoing_qos":         "false",
}

// TestImageAcceptsTheGeneratedConfiguration asks the pinned image's own
// --test-config about the file the operator generates, plain and with TLS, with
// every allowed spec.config directive appended. A generated directive the image
// does not know, or an allowlist entry it refuses, turns red here rather than in
// the config-check init container of every broker pod.
func TestImageAcceptsTheGeneratedConfiguration(t *testing.T) {
	t.Parallel()

	directives := make([]string, 0, len(allowlistSamples))
	for directive := range builder.AllowedConfigDirectives {
		value, ok := allowlistSamples[directive]
		require.True(t, ok, "allowlisted directive %q has no sample value to test the image with", directive)
		directives = append(directives, directive+" "+value)
	}
	require.Len(t, allowlistSamples, len(builder.AllowedConfigDirectives), "a sample for a directive that is not allowed")
	sort.Strings(directives)

	for _, tls := range []bool{false, true} {
		m := &mkov1.Mosquitto{
			ObjectMeta: metav1.ObjectMeta{Name: "probe", Namespace: "probe"},
			Spec:       mkov1.MosquittoSpec{Replicas: 1, Config: strings.Join(directives, "\n")},
		}
		if tls {
			m.Spec.TLS = &mkov1.MosquittoTLS{SecretName: "probe-tls"}
		}
		require.NoError(t, builder.ValidateSpecConfig(m))

		// --test-config opens no TLS file and saves its empty database to the
		// persistence path (M19), which therefore has to exist and be writable.
		script := fmt.Sprintf(`mkdir -p /tmp/c /mosquitto/data 2>/dev/null; cat > /tmp/c/mosquitto.conf <<'CONF'
%s
CONF
/usr/sbin/mosquitto -c /tmp/c/mosquitto.conf --test-config 2>&1; echo "rc=$?"`, builder.GenerateMosquittoConf(m))

		out := runInImageAs(t, testimages.MosquittoImage, "1883:1883", script)
		assert.Contains(t, out, "Configuration file is OK.", "tls=%v:\n%s", tls, out)
		assert.Contains(t, out, "rc=0", "tls=%v:\n%s", tls, out)
	}
}
