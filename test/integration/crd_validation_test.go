//go:build integration

package integration

// The CRD's own behaviour. These assertions are only possible against a real API
// server: the kubebuilder markers on api/v1 become OpenAPI schema, and nothing in
// a unit test exercises the schema.

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
)

// TestIntegration_CRD_AppliesTheDefaults covers the two defaulted fields. They
// are what makes an empty spec a valid, single-replica broker with untouched
// scheduling.
func TestIntegration_CRD_AppliesTheDefaults(t *testing.T) {
	ns := newNamespace(t)
	name := "defaults"

	// The assertion is on what Create returned, which is what the API server
	// stored: reading it back would go through the manager cache and could only
	// weaken the statement.
	stored := createMosquitto(t, ns, name, mkov1.MosquittoSpec{})

	assert.Equal(t, int32(1), stored.Spec.Replicas)
	assert.Equal(t, mkov1.AntiAffinityModeOff, stored.Spec.AntiAffinity)
}

// TestIntegration_CRD_RejectsInvalidSpecs pins the validation markers. Each of
// these would otherwise reach the builder, where the failure is a pod that never
// starts instead of a rejected apply.
func TestIntegration_CRD_RejectsInvalidSpecs(t *testing.T) {
	ns := newNamespace(t)

	cases := map[string]mkov1.MosquittoSpec{
		"more replicas than the maximum": {Replicas: 10},
		"an unknown anti-affinity mode":  {AntiAffinity: "sometimes"},
		"an empty TLS secret name":       {TLS: &mkov1.MosquittoTLS{SecretName: ""}},
		"an empty storage size":          {Storage: &mkov1.MosquittoStorage{Size: ""}},
	}

	for reason, spec := range cases {
		t.Run(reason, func(t *testing.T) {
			m := &mkov1.Mosquitto{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "invalid-", Namespace: ns},
				Spec:       spec,
			}
			err := k8sClient.Create(testCtx, m)
			require.Error(t, err, "the API server accepted %s", reason)
		})
	}
}

// newUser builds a MosquittoUser with one ACL entry.
func newUser(namespace, name, topic, access string) *mkov1.MosquittoUser {
	return &mkov1.MosquittoUser{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: mkov1.MosquittoUserSpec{
			BrokerRef:         mkov1.MosquittoBrokerRef{Name: "broker"},
			CredentialsSecret: mkov1.MosquittoCredentialsSecret{Name: name + "-mqtt"},
			ACLs:              []mkov1.MosquittoACL{{Topic: topic, Access: access}},
		},
	}
}

// TestIntegration_CRD_UserDefaultsTheBasicAuthKeys: an empty usernameKey and
// passwordKey become the keys of a kubernetes.io/basic-auth Secret (ADR 0013 D2).
func TestIntegration_CRD_UserDefaultsTheBasicAuthKeys(t *testing.T) {
	ns := newNamespace(t)
	user := newUser(ns, "z2m", "zigbee2mqtt/#", mkov1.AccessReadWrite)
	require.NoError(t, k8sClient.Create(testCtx, user))

	assert.Equal(t, "username", user.Spec.CredentialsSecret.UsernameKey)
	assert.Equal(t, "password", user.Spec.CredentialsSecret.PasswordKey)
}

// TestIntegration_CRD_UserRefusesWhatCELCanSee: the shape checks of ADR 0013
// D3 and D4 reject the apply. The render-time checks repeat them as the
// authority (internal/auth), because CEL can be bypassed by an older CRD.
func TestIntegration_CRD_UserRefusesWhatCELCanSee(t *testing.T) {
	ns := newNamespace(t)

	tests := []struct {
		name, topic, access, wantMessage string
	}{
		{"the $SYS tree", "$SYS/#", mkov1.AccessRead, "a topic starting with $ is reserved"},
		{"the dynamic-security control topic", "$CONTROL/dynamic-security/v1", mkov1.AccessWrite, "a topic starting with $ is reserved"},
		{"an unknown access mode", "home/#", "deny", "Unsupported value"},
		{"a line break that would inject a second ACL line", "home/#\ntopic readwrite #", mkov1.AccessRead, "control character"},
		{"an empty topic", "", mkov1.AccessRead, "spec.acls[0].topic"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := k8sClient.Create(testCtx, newUser(ns, fmt.Sprintf("refused-%d", i), tt.topic, tt.access))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantMessage)
		})
	}
}
