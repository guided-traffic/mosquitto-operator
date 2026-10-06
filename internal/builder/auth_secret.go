package builder

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/common"
)

// BuildAuthSecret builds <name>-auth, the one Secret per broker that carries
// its whole rendered credentials (ADR 0014 D2): hashes, never a plaintext, in
// the keys the payload chose. It is part of no pod hash: a change reaches the
// broker through the reloader, not through a roll (D4).
func BuildAuthSecret(m *mkov1.Mosquitto, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      common.AuthSecretName(m),
			Namespace: m.Namespace,
			Labels:    common.BaseLabels(m, ResolveImage(m)),
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
}
