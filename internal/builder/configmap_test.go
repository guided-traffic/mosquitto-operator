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

func TestGenerateMosquittoConf_PlainListener(t *testing.T) {
	conf := GenerateMosquittoConf(newMosquitto())

	assert.Contains(t, conf, "listener 1883")
	assert.NotContains(t, conf, "listener 8883")
	assert.NotContains(t, conf, "certfile")
	assert.NotContains(t, conf, "keyfile")
	assert.Contains(t, conf, "persistence true")
	assert.Contains(t, conf, "persistence_location /mosquitto/data/")
	assert.Contains(t, conf, "log_dest stdout")
	assert.Contains(t, conf, "allow_anonymous true")
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

func TestGenerateMosquittoConf_SpecConfigIsAppendedVerbatim(t *testing.T) {
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
		{"a user override lands after the generated line", "allow_anonymous false",
			"allow_anonymous false", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMosquitto(func(m *mkov1.Mosquitto) { m.Spec.Config = tt.config })
			conf := GenerateMosquittoConf(m)

			if !tt.wantSection {
				assert.NotContains(t, conf, "spec.config, appended verbatim")
				return
			}

			assert.Contains(t, conf, "spec.config, appended verbatim")
			assert.Contains(t, conf, tt.wantContain)
			assert.Greater(t, strings.Index(conf, tt.wantContain), strings.Index(conf, "allow_anonymous true"),
				"spec.config must come last so it can override a generated directive")
		})
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
