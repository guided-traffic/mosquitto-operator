//go:build integration

package integration

// The CRD's own behaviour. These assertions are only possible against a real API
// server: the kubebuilder markers on api/v1 become OpenAPI schema, and nothing in
// a unit test exercises the schema.

import (
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
