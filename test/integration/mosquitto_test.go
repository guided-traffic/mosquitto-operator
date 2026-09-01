//go:build integration

package integration

// The reconcile pass against a real API server: every object is created, owned,
// and updated when the spec changes -- and an object the Mosquitto does not own
// is left alone.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/builder"
)

// fixtureImage is never pulled: envtest runs no kubelet. It only has to be a
// value the assertions can recognise, which is why it is not the pinned image
// from test/testimages.
const fixtureImage = "example.test/mosquitto:integration"

// TestIntegration_Reconcile_CreatesEveryOwnedObject is the shape of one pass: a
// ConfigMap, two Services and a StatefulSet, each carrying a controller
// reference, which is what makes deleting the Mosquitto enough to clean up.
func TestIntegration_Reconcile_CreatesEveryOwnedObject(t *testing.T) {
	ns := newNamespace(t)
	name := "owned"

	createMosquitto(t, ns, name, mkov1.MosquittoSpec{Replicas: 2, Image: fixtureImage})

	t.Run("the ConfigMap carries the generated configuration", func(t *testing.T) {
		cm := &corev1.ConfigMap{}
		eventuallyGet(t, ns, name+"-config", cm)

		assert.True(t, isControlledBy(cm, name), "the ConfigMap is not owned by the Mosquitto")
		assert.Contains(t, cm.Data["mosquitto.conf"], "listener 1883")
		assert.Contains(t, cm.Data["mosquitto.conf"], "persistence_location /mosquitto/data/")
	})

	t.Run("both Services exist and select the broker pods", func(t *testing.T) {
		selector := map[string]string{
			"app.kubernetes.io/name":       "mosquitto",
			"app.kubernetes.io/instance":   name,
			"app.kubernetes.io/managed-by": "mosquitto-operator",
		}

		client := &corev1.Service{}
		eventuallyGet(t, ns, name, client)
		assert.True(t, isControlledBy(client, name), "the client Service is not owned by the Mosquitto")
		assert.Equal(t, selector, client.Spec.Selector)
		require.Len(t, client.Spec.Ports, 1)
		assert.Equal(t, int32(1883), client.Spec.Ports[0].Port)
		assert.Equal(t, "mqtt", client.Spec.Ports[0].Name)

		headless := &corev1.Service{}
		eventuallyGet(t, ns, name+"-headless", headless)
		assert.True(t, isControlledBy(headless, name), "the headless Service is not owned by the Mosquitto")
		assert.Equal(t, corev1.ClusterIPNone, headless.Spec.ClusterIP)
		assert.True(t, headless.Spec.PublishNotReadyAddresses,
			"per-pod DNS has to resolve before the pod passes its readiness probe")
	})

	t.Run("the StatefulSet asks for the requested replicas and image", func(t *testing.T) {
		sts := waitForStatefulSet(t, ns, name)

		assert.True(t, isControlledBy(sts, name), "the StatefulSet is not owned by the Mosquitto")
		require.NotNil(t, sts.Spec.Replicas)
		assert.Equal(t, int32(2), *sts.Spec.Replicas)
		assert.Equal(t, name+"-headless", sts.Spec.ServiceName)

		require.Len(t, sts.Spec.Template.Spec.Containers, 1)
		assert.Equal(t, fixtureImage, sts.Spec.Template.Spec.Containers[0].Image)
	})

	t.Run("the phase reports the missing pods rather than success", func(t *testing.T) {
		// envtest runs no kubelet, so status.readyReplicas of the StatefulSet
		// stays 0 forever. Pending is the honest answer, and asserting it here is
		// what proves the phase is computed from the workload and not from the
		// fact that the writes succeeded.
		m := waitForPhase(t, ns, name, mkov1.PhasePending)

		assert.Equal(t, int32(0), m.Status.ReadyReplicas)
		assert.Equal(t, m.Generation, m.Status.ObservedGeneration)

		require.Len(t, m.Status.Conditions, 1)
		assert.Equal(t, mkov1.ConditionTypeReady, m.Status.Conditions[0].Type)
		assert.Equal(t, metav1.ConditionFalse, m.Status.Conditions[0].Status)
	})
}

// TestIntegration_Reconcile_ReadyReplicasDriveThePhase closes the other half of
// the status: the operator watches the StatefulSet it owns, so a readiness change
// it did not cause still reaches the CR.
func TestIntegration_Reconcile_ReadyReplicasDriveThePhase(t *testing.T) {
	ns := newNamespace(t)
	name := "ready"

	createMosquitto(t, ns, name, mkov1.MosquittoSpec{Replicas: 1, Image: fixtureImage})
	waitForStatefulSet(t, ns, name)
	waitForPhase(t, ns, name, mkov1.PhasePending)

	// Stand in for the StatefulSet controller, which envtest does not run.
	// Retried, because the write races the operator's own updates to the object.
	key := types.NamespacedName{Namespace: ns, Name: name}
	require.Eventually(t, func() bool {
		sts := &appsv1.StatefulSet{}
		if err := k8sClient.Get(testCtx, key, sts); err != nil {
			return false
		}
		sts.Status.ObservedGeneration = sts.Generation
		sts.Status.Replicas = 1
		sts.Status.ReadyReplicas = 1
		return k8sClient.Status().Update(testCtx, sts) == nil
	}, eventuallyTimeout, eventuallyInterval, "could not mark the StatefulSet ready")

	m := waitForPhase(t, ns, name, mkov1.PhaseReady)
	assert.Equal(t, int32(1), m.Status.ReadyReplicas)
	require.Len(t, m.Status.Conditions, 1)
	assert.Equal(t, metav1.ConditionTrue, m.Status.Conditions[0].Status)
	assert.Equal(t, "AllReplicasReady", m.Status.Conditions[0].Reason)
}

// TestIntegration_Reconcile_ConfigChangeReachesThePodTemplate covers the reason
// the config hash annotation exists: Mosquitto reads its configuration once at
// startup, so a ConfigMap update alone would never reach a running broker.
func TestIntegration_Reconcile_ConfigChangeReachesThePodTemplate(t *testing.T) {
	ns := newNamespace(t)
	name := "config"

	createMosquitto(t, ns, name, mkov1.MosquittoSpec{Replicas: 1, Image: fixtureImage})
	before := waitForStatefulSet(t, ns, name).Spec.Template.Annotations[builder.AnnotationConfigHash]
	require.NotEmpty(t, before, "the pod template carries no config hash")

	const extra = "max_keepalive 120"
	m := getMosquitto(t, ns, name)
	m.Spec.Config = extra
	require.NoError(t, k8sClient.Update(testCtx, m))

	require.Eventually(t, func() bool {
		cm := &corev1.ConfigMap{}
		if err := k8sClient.Get(testCtx,
			types.NamespacedName{Namespace: ns, Name: name + "-config"}, cm); err != nil {
			return false
		}
		return strings.Contains(cm.Data["mosquitto.conf"], extra)
	}, eventuallyTimeout, eventuallyInterval, "spec.config never reached the ConfigMap")

	require.Eventually(t, func() bool {
		sts := &appsv1.StatefulSet{}
		if err := k8sClient.Get(testCtx, types.NamespacedName{Namespace: ns, Name: name}, sts); err != nil {
			return false
		}
		return sts.Spec.Template.Annotations[builder.AnnotationConfigHash] != before
	}, eventuallyTimeout, eventuallyInterval,
		"the config hash did not change, so the running brokers would keep the old configuration")
}

// TestIntegration_Reconcile_RefusesAnObjectItDoesNotOwn is the adoption guard.
// Every managed name is derived from the CR name, so a Mosquitto called like an
// existing ConfigMap would otherwise overwrite somebody else's data.
func TestIntegration_Reconcile_RefusesAnObjectItDoesNotOwn(t *testing.T) {
	ns := newNamespace(t)
	name := "foreign"

	foreign := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-config", Namespace: ns},
		Data:       map[string]string{"mosquitto.conf": "# written by somebody else"},
	}
	require.NoError(t, k8sClient.Create(testCtx, foreign))

	createMosquitto(t, ns, name, mkov1.MosquittoSpec{Replicas: 1, Image: fixtureImage})

	m := waitForPhase(t, ns, name, mkov1.PhaseFailed)
	require.Len(t, m.Status.Conditions, 1)
	assert.Equal(t, "ReconcileFailed", m.Status.Conditions[0].Reason)
	assert.Contains(t, m.Status.Conditions[0].Message, "not owned by this Mosquitto")

	kept := &corev1.ConfigMap{}
	require.NoError(t, k8sClient.Get(testCtx,
		types.NamespacedName{Namespace: ns, Name: name + "-config"}, kept))
	assert.Equal(t, "# written by somebody else", kept.Data["mosquitto.conf"],
		"the foreign ConfigMap was overwritten")

	sts := &appsv1.StatefulSet{}
	err := k8sClient.Get(testCtx, types.NamespacedName{Namespace: ns, Name: name}, sts)
	assert.Error(t, err, "the pass must stop at the refusal instead of building on it")
}

// TestIntegration_Storage_RendersAVolumeClaimTemplate proves the PVC template is
// accepted by the API server. It is the field that cannot be corrected later:
// volumeClaimTemplates are immutable, so a template the API server rejects means
// the StatefulSet has to be deleted by hand.
func TestIntegration_Storage_RendersAVolumeClaimTemplate(t *testing.T) {
	ns := newNamespace(t)
	name := "stored"

	createMosquitto(t, ns, name, mkov1.MosquittoSpec{
		Replicas: 1,
		Image:    fixtureImage,
		Storage:  &mkov1.MosquittoStorage{Size: "1Gi"},
	})

	sts := waitForStatefulSet(t, ns, name)

	require.Len(t, sts.Spec.VolumeClaimTemplates, 1)
	template := sts.Spec.VolumeClaimTemplates[0]
	assert.Equal(t, "data", template.Name)
	assert.Equal(t, resource.MustParse("1Gi"), template.Spec.Resources.Requests[corev1.ResourceStorage])

	for _, volume := range sts.Spec.Template.Spec.Volumes {
		assert.NotEqual(t, "data", volume.Name,
			"the PVC template and an emptyDir of the same name would collide")
	}
}
