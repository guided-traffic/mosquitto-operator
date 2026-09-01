//go:build integration

package integration

// TLS as the operator implements it: it consumes a Secret somebody else owns and
// switches the single listener to MQTTS. Whether the broker then completes a TLS
// handshake needs a running pod and lives in test/e2e.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
)

// TestIntegration_TLS_MountsTheSecretAndMovesTheListener is the whole feature:
// one extra volume, one extra read-only mount, and port 8883 instead of 1883.
func TestIntegration_TLS_MountsTheSecretAndMovesTheListener(t *testing.T) {
	ns := newNamespace(t)
	name := "tls"
	secretName := "broker-tls"

	require.NoError(t, k8sClient.Create(testCtx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns},
		Type:       corev1.SecretTypeTLS,
		StringData: map[string]string{"tls.crt": "not-a-certificate", "tls.key": "not-a-key"},
	}))

	createMosquitto(t, ns, name, mkov1.MosquittoSpec{
		Replicas: 1,
		Image:    fixtureImage,
		TLS:      &mkov1.MosquittoTLS{SecretName: secretName},
	})

	sts := waitForStatefulSet(t, ns, name)

	var mountedSecret string
	for _, volume := range sts.Spec.Template.Spec.Volumes {
		if volume.Secret != nil {
			mountedSecret = volume.Secret.SecretName
		}
	}
	assert.Equal(t, secretName, mountedSecret, "the referenced secret is not a volume")

	require.Len(t, sts.Spec.Template.Spec.Containers, 1)
	container := sts.Spec.Template.Spec.Containers[0]

	var tlsMount *corev1.VolumeMount
	for i := range container.VolumeMounts {
		if container.VolumeMounts[i].Name == "tls" {
			tlsMount = &container.VolumeMounts[i]
		}
	}
	require.NotNil(t, tlsMount, "the TLS volume is not mounted into the broker container")
	assert.Equal(t, "/mosquitto/tls", tlsMount.MountPath)
	assert.True(t, tlsMount.ReadOnly)

	require.Len(t, container.Ports, 1, "TLS replaces the plain listener, it does not add to it")
	assert.Equal(t, "mqtts", container.Ports[0].Name)
	assert.Equal(t, int32(8883), container.Ports[0].ContainerPort)

	cm := &corev1.ConfigMap{}
	eventuallyGet(t, ns, name+"-config", cm)
	assert.Contains(t, cm.Data["mosquitto.conf"], "listener 8883")
	assert.Contains(t, cm.Data["mosquitto.conf"], "certfile /mosquitto/tls/tls.crt")
	assert.Contains(t, cm.Data["mosquitto.conf"], "keyfile /mosquitto/tls/tls.key")
	assert.NotContains(t, cm.Data["mosquitto.conf"], "listener 1883")

	for _, svcName := range []string{name, name + "-headless"} {
		svc := &corev1.Service{}
		eventuallyGet(t, ns, svcName, svc)
		require.Len(t, svc.Spec.Ports, 1, "Service %s", svcName)
		assert.Equal(t, int32(8883), svc.Spec.Ports[0].Port, "Service %s", svcName)
	}
}

// TestIntegration_TLS_DoesNotWaitForTheSecret documents where the ownership of
// the TLS material sits. The operator writes the StatefulSet whether or not the
// named Secret exists; a missing one leaves the pod unable to mount its volume,
// which is a kubelet-level error on the pod rather than a reconcile failure.
//
// Security consequence to be aware of: nothing here watches the Secret. Replacing
// its contents does not restart the brokers, so a rotation has to be followed by
// a restart of the StatefulSet -- the operator will not do it.
func TestIntegration_TLS_DoesNotWaitForTheSecret(t *testing.T) {
	ns := newNamespace(t)
	name := "tls-missing"

	createMosquitto(t, ns, name, mkov1.MosquittoSpec{
		Replicas: 1,
		Image:    fixtureImage,
		TLS:      &mkov1.MosquittoTLS{SecretName: "does-not-exist"},
	})

	sts := waitForStatefulSet(t, ns, name)
	assert.Equal(t, int32(8883), sts.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort)

	secret := &corev1.Secret{}
	err := k8sClient.Get(testCtx, types.NamespacedName{Namespace: ns, Name: "does-not-exist"}, secret)
	require.Error(t, err, "the operator must not create the TLS secret itself")
}
