package builder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/common"
)

func TestBuildHeadlessService(t *testing.T) {
	m := newMosquitto()
	svc := BuildHeadlessService(m)

	assert.Equal(t, "broker-headless", svc.Name)
	assert.Equal(t, "messaging", svc.Namespace)
	assert.Equal(t, corev1.ClusterIPNone, svc.Spec.ClusterIP)
	assert.True(t, svc.Spec.PublishNotReadyAddresses,
		"per-pod DNS has to resolve before a pod is ready, or a starting broker has no name")
	assert.Equal(t, common.SelectorLabels(m), svc.Spec.Selector)
	assert.Equal(t, common.BaseLabels(m, DefaultImage), svc.Labels)
}

func TestBuildClientService(t *testing.T) {
	m := newMosquitto()
	svc := BuildClientService(m)

	assert.Equal(t, "broker", svc.Name)
	assert.Equal(t, "messaging", svc.Namespace)
	assert.Equal(t, corev1.ServiceTypeClusterIP, svc.Spec.Type)
	assert.Empty(t, svc.Spec.ClusterIP, "the client Service is a normal ClusterIP, not a headless one")
	assert.False(t, svc.Spec.PublishNotReadyAddresses,
		"clients must only reach brokers that passed their readiness probe")
	assert.Equal(t, common.SelectorLabels(m), svc.Spec.Selector)
}

func TestServicePortsFollowTLS(t *testing.T) {
	tests := []struct {
		name     string
		m        *mkov1.Mosquitto
		wantPort int32
		wantName string
	}{
		{"plain MQTT", newMosquitto(), MQTTPort, MQTTPortName},
		{"MQTTS", newMosquitto(withTLS("broker-tls")), MQTTSPort, MQTTSPortName},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for name, svc := range map[string]*corev1.Service{
				"headless": BuildHeadlessService(tt.m),
				"client":   BuildClientService(tt.m),
			} {
				require.Len(t, svc.Spec.Ports, 1, "%s Service exposes exactly one listener", name)
				port := svc.Spec.Ports[0]

				assert.Equal(t, tt.wantName, port.Name)
				assert.Equal(t, tt.wantPort, port.Port)
				assert.Equal(t, corev1.ProtocolTCP, port.Protocol)
				assert.Equal(t, tt.wantName, port.TargetPort.StrVal,
					"the target is the named container port, so the pod decides the number")
			}
		})
	}
}
