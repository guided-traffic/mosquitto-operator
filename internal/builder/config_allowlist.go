package builder

import (
	"fmt"
	"strings"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
)

// AllowedConfigDirectives are the directives spec.config may carry (ADR 0008
// D15): tuning only - limits, queues, keepalive, persistence intervals, log
// types. The list was taken from mosquitto.conf(5) of the pinned 2.1.2 and each
// entry checked with that image's --test-config
// (docs/developer/broker-behaviour.md M26). Every directive not listed is
// refused, a listener, a plugin, anything about anonymous access or client
// identity, a bridge and every file path above all: one overlooked directive of
// those opens a second, anonymous, ACL-free listener (M15). Extending the list
// is not breaking; it costs an operator release.
var AllowedConfigDirectives = map[string]bool{
	"autosave_interval":            true,
	"autosave_on_changes":          true,
	"connection_messages":          true,
	"global_max_clients":           true,
	"global_max_connections":       true,
	"log_timestamp":                true,
	"log_timestamp_format":         true,
	"log_type":                     true,
	"max_connections":              true,
	"max_inflight_bytes":           true,
	"max_inflight_messages":        true,
	"max_keepalive":                true,
	"max_packet_size":              true,
	"max_qos":                      true,
	"max_queued_bytes":             true,
	"max_queued_messages":          true,
	"max_topic_alias":              true,
	"max_topic_alias_broker":       true,
	"memory_limit":                 true,
	"persistent_client_expiration": true,
	"queue_qos0_messages":          true,
	"retain_available":             true,
	"retain_expiry_interval":       true,
	"set_tcp_nodelay":              true,
	"sys_interval":                 true,
	"upgrade_outgoing_qos":         true,
}

// ValidateSpecConfig checks spec.config line by line against
// AllowedConfigDirectives. Blank lines and comments pass; any other line passes
// only when its first word is an allowed directive. The error names the first
// refused line by its number within spec.config. Values are not checked: the
// config-check init container runs the broker's own --test-config on them.
func ValidateSpecConfig(m *mkov1.Mosquitto) error {
	for i, line := range strings.Split(m.Spec.Config, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if !AllowedConfigDirectives[fields[0]] {
			return fmt.Errorf("spec.config line %d (%q): the directive %q is not on the allowlist of tuning directives",
				i+1, strings.TrimSpace(line), fields[0])
		}
	}
	return nil
}
