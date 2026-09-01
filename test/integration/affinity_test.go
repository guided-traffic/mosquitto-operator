//go:build integration

package integration

// Anti-affinity, as the API server stores it. Whether the pods then actually land
// on distinct nodes is a scheduler question and lives in test/e2e.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
)

// TestIntegration_AntiAffinity_OffRendersNoAffinityBlock pins the default. An
// empty affinity block would be harmless to the scheduler but would still change
// the pod template of every existing broker on an operator upgrade, which is a
// rolling restart nobody asked for.
func TestIntegration_AntiAffinity_OffRendersNoAffinityBlock(t *testing.T) {
	ns := newNamespace(t)
	name := "aa-off"

	createMosquitto(t, ns, name, mkov1.MosquittoSpec{
		Replicas:     1,
		Image:        fixtureImage,
		AntiAffinity: mkov1.AntiAffinityModeOff,
	})

	sts := waitForStatefulSet(t, ns, name)
	assert.Nil(t, sts.Spec.Template.Spec.Affinity)
}

// TestIntegration_AntiAffinity_SoftRendersAPreference checks the term the
// scheduler weighs but never enforces.
func TestIntegration_AntiAffinity_SoftRendersAPreference(t *testing.T) {
	ns := newNamespace(t)
	name := "aa-soft"

	createMosquitto(t, ns, name, mkov1.MosquittoSpec{
		Replicas:     3,
		Image:        fixtureImage,
		AntiAffinity: mkov1.AntiAffinityModeSoft,
	})

	sts := waitForStatefulSet(t, ns, name)
	affinity := sts.Spec.Template.Spec.Affinity
	require.NotNil(t, affinity)
	require.NotNil(t, affinity.PodAntiAffinity)
	assert.Empty(t, affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution,
		"soft must never block scheduling")

	preferred := affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	require.Len(t, preferred, 1)
	assert.Equal(t, mkov1.AntiAffinityWeight, preferred[0].Weight)
	assert.Equal(t, mkov1.AntiAffinityTopologyKey, preferred[0].PodAffinityTerm.TopologyKey)
	assertRepelsOnlyItsOwnBrokers(t, preferred[0].PodAffinityTerm.LabelSelector.MatchLabels, name)
}

// TestIntegration_AntiAffinity_HardRendersARequirement checks the term that can
// leave surplus pods Pending, which is the documented cost of the mode.
func TestIntegration_AntiAffinity_HardRendersARequirement(t *testing.T) {
	ns := newNamespace(t)
	name := "aa-hard"

	createMosquitto(t, ns, name, mkov1.MosquittoSpec{
		Replicas:     3,
		Image:        fixtureImage,
		AntiAffinity: mkov1.AntiAffinityModeHard,
	})

	sts := waitForStatefulSet(t, ns, name)
	affinity := sts.Spec.Template.Spec.Affinity
	require.NotNil(t, affinity)
	require.NotNil(t, affinity.PodAntiAffinity)
	assert.Empty(t, affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution,
		"hard mode must not additionally emit a preference")

	required := affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	require.Len(t, required, 1)
	assert.Equal(t, mkov1.AntiAffinityTopologyKey, required[0].TopologyKey)
	assertRepelsOnlyItsOwnBrokers(t, required[0].LabelSelector.MatchLabels, name)
}

// assertRepelsOnlyItsOwnBrokers checks the selector of an anti-affinity term. A
// selector without the instance label would spread the brokers of every Mosquitto
// in the namespace against each other and starve the second one of nodes.
func assertRepelsOnlyItsOwnBrokers(t *testing.T, matchLabels map[string]string, instance string) {
	t.Helper()

	assert.Equal(t, map[string]string{
		"app.kubernetes.io/name":       "mosquitto",
		"app.kubernetes.io/instance":   instance,
		"app.kubernetes.io/managed-by": "mosquitto-operator",
	}, matchLabels)
}
