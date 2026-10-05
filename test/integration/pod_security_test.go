//go:build integration

package integration

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/builder"
)

// restrictedNamespace creates a namespace whose PodSecurity admission enforces
// the restricted profile at the latest version.
func restrictedNamespace(t *testing.T) string {
	t.Helper()

	name := fmt.Sprintf("mko-int-restricted-%d", namespaceCounter.Add(1))
	require.NoError(t, k8sClient.Create(testCtx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{
			"pod-security.kubernetes.io/enforce":         "restricted",
			"pod-security.kubernetes.io/enforce-version": "latest",
		}},
	}))
	return name
}

// brokerPod turns the pod template of the StatefulSet the builder renders for m
// into a Pod, the object PodSecurity admission actually judges. A claim template
// becomes the volume the StatefulSet controller would add for it.
func brokerPod(t *testing.T, m *mkov1.Mosquitto) *corev1.Pod {
	t.Helper()

	sts, err := builder.BuildStatefulSet(m, builder.PodOptions{ReloaderImage: testReloaderImage})
	require.NoError(t, err)
	spec := *sts.Spec.Template.Spec.DeepCopy()
	for _, claim := range sts.Spec.VolumeClaimTemplates {
		spec.Volumes = append(spec.Volumes, corev1.Volume{
			Name: claim.Name,
			VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: claim.Name + "-" + m.Name + "-0",
			}},
		})
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        m.Name + "-0",
			Namespace:   m.Namespace,
			Labels:      sts.Spec.Template.Labels,
			Annotations: sts.Spec.Template.Annotations,
		},
		Spec: spec,
	}
}

// TestIntegration_PodSecurity_RestrictedAdmitsEveryShape is the guard of ADR
// 0012 D4: the API server's own PodSecurity admission, enforcing the restricted
// profile, judges the pod of every shape the builder renders - not a list of
// field values restated by hand. The pods are created as a dry run, so admission
// runs and nothing is stored.
//
// The first subtest is the control: a pod the profile must refuse is refused.
// Without it, an API server that enforced nothing would let every shape pass and
// the guard would be green over nothing (ADR 0010 D4).
func TestIntegration_PodSecurity_RestrictedAdmitsEveryShape(t *testing.T) {
	namespace := restrictedNamespace(t)
	require.NoError(t, k8sClient.Create(testCtx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: namespace},
	}), "envtest runs no controller that creates the default ServiceAccount")

	base := func(mutators ...func(*mkov1.Mosquitto)) *mkov1.Mosquitto {
		m := &mkov1.Mosquitto{
			ObjectMeta: metav1.ObjectMeta{Name: "broker", Namespace: namespace},
			Spec:       mkov1.MosquittoSpec{Replicas: 1},
		}
		for _, mutate := range mutators {
			mutate(m)
		}
		return m
	}
	withTLS := func(m *mkov1.Mosquitto) { m.Spec.TLS = &mkov1.MosquittoTLS{SecretName: "broker-tls"} }
	withStorage := func(m *mkov1.Mosquitto) { m.Spec.Storage = &mkov1.MosquittoStorage{Size: "1Gi"} }
	withHardAntiAffinity := func(m *mkov1.Mosquitto) { m.Spec.AntiAffinity = mkov1.AntiAffinityModeHard }
	withMetrics := func(m *mkov1.Mosquitto) { m.Spec.Metrics = &mkov1.MosquittoMetrics{Enabled: true} }

	t.Run("control: a pod the restricted profile forbids is refused", func(t *testing.T) {
		pod := brokerPod(t, base())
		pod.Spec.Containers[0].SecurityContext.AllowPrivilegeEscalation = ptr.To(true)

		err := k8sClient.Create(testCtx, pod, client.DryRunAll)
		require.Error(t, err, "this API server does not enforce PodSecurity, so the subtests below prove nothing")
		require.Contains(t, err.Error(), "violates PodSecurity")
	})

	shapes := []struct {
		name string
		m    *mkov1.Mosquitto
	}{
		{"plain listener, ephemeral persistence", base()},
		{"TLS secret mounted", base(withTLS)},
		{"PVC-backed persistence", base(withStorage)},
		{"TLS and storage together", base(withTLS, withStorage)},
		{"hard anti-affinity", base(withHardAntiAffinity)},
		{"metrics exporter, with TLS", base(withMetrics, withTLS)},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			require.NoError(t, k8sClient.Create(testCtx, brokerPod(t, shape.m), client.DryRunAll))
		})
	}
}
