package builder

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/common"
)

// BuildPodAntiAffinity builds the pod anti-affinity term for the broker pods.
//
// The term repels only the broker pods of the same Mosquitto — the selector is
// the StatefulSet's own selector — so a second Mosquitto in the same namespace is
// unaffected by this one's spreading.
//
// Returns nil when the mode is off (the default), so scheduling stays untouched
// unless the user opts in and no empty affinity block ends up in the pod
// template.
func BuildPodAntiAffinity(m *mkov1.Mosquitto) *corev1.Affinity {
	mode := m.AntiAffinityMode()
	if mode == mkov1.AntiAffinityModeOff {
		return nil
	}

	term := corev1.PodAffinityTerm{
		LabelSelector: &metav1.LabelSelector{
			MatchLabels: common.SelectorLabels(m),
		},
		TopologyKey: mkov1.AntiAffinityTopologyKey,
	}

	antiAffinity := &corev1.PodAntiAffinity{}
	if mode == mkov1.AntiAffinityModeHard {
		antiAffinity.RequiredDuringSchedulingIgnoredDuringExecution = []corev1.PodAffinityTerm{term}
	} else {
		antiAffinity.PreferredDuringSchedulingIgnoredDuringExecution = []corev1.WeightedPodAffinityTerm{{
			Weight:          mkov1.AntiAffinityWeight,
			PodAffinityTerm: term,
		}}
	}

	return &corev1.Affinity{PodAntiAffinity: antiAffinity}
}
