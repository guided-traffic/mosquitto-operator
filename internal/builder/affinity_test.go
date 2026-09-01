package builder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/common"
)

func withAntiAffinity(mode string) func(*mkov1.Mosquitto) {
	return func(m *mkov1.Mosquitto) { m.Spec.AntiAffinity = mode }
}

func TestBuildPodAntiAffinity_OffRendersNothing(t *testing.T) {
	tests := []struct {
		name string
		mode string
	}{
		{"explicit off", mkov1.AntiAffinityModeOff},
		{"unset defaults to off", ""},
		{"an unknown mode degrades to off", "strict"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Nil(t, BuildPodAntiAffinity(newMosquitto(withAntiAffinity(tt.mode))),
				"an empty affinity block would still change the pod template for nothing")
		})
	}
}

func TestBuildPodAntiAffinity_Soft(t *testing.T) {
	m := newMosquitto(withAntiAffinity(mkov1.AntiAffinityModeSoft))

	affinity := BuildPodAntiAffinity(m)
	require.NotNil(t, affinity)
	require.NotNil(t, affinity.PodAntiAffinity)

	preferred := affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	require.Len(t, preferred, 1)
	assert.Empty(t, affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution,
		"soft must never make a pod unschedulable")

	assert.Equal(t, mkov1.AntiAffinityWeight, preferred[0].Weight)
	assert.Equal(t, mkov1.AntiAffinityTopologyKey, preferred[0].PodAffinityTerm.TopologyKey)
	assert.Equal(t, common.SelectorLabels(m), preferred[0].PodAffinityTerm.LabelSelector.MatchLabels)
}

func TestBuildPodAntiAffinity_Hard(t *testing.T) {
	m := newMosquitto(withAntiAffinity(mkov1.AntiAffinityModeHard))

	affinity := BuildPodAntiAffinity(m)
	require.NotNil(t, affinity)
	require.NotNil(t, affinity.PodAntiAffinity)

	required := affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	require.Len(t, required, 1)
	assert.Empty(t, affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution)

	assert.Equal(t, mkov1.AntiAffinityTopologyKey, required[0].TopologyKey)
	assert.Equal(t, common.SelectorLabels(m), required[0].LabelSelector.MatchLabels)
}

// TestAntiAffinityRepelsOnlyTheSameResource is what keeps a second Mosquitto in
// the same namespace from being spread by this one's term: the selector is the
// StatefulSet's own selector, which carries the instance name.
func TestAntiAffinityRepelsOnlyTheSameResource(t *testing.T) {
	first := newMosquitto(withAntiAffinity(mkov1.AntiAffinityModeHard))
	second := newMosquitto(withAntiAffinity(mkov1.AntiAffinityModeHard))
	second.Name = "other-broker"

	firstSelector := BuildPodAntiAffinity(first).PodAntiAffinity.
		RequiredDuringSchedulingIgnoredDuringExecution[0].LabelSelector.MatchLabels
	secondSelector := BuildPodAntiAffinity(second).PodAntiAffinity.
		RequiredDuringSchedulingIgnoredDuringExecution[0].LabelSelector.MatchLabels

	assert.NotEqual(t, firstSelector, secondSelector)
	assert.Equal(t, "broker", firstSelector[common.LabelInstance])
	assert.Equal(t, "other-broker", secondSelector[common.LabelInstance])
}
