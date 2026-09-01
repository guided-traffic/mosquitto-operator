//go:build e2e

package e2e

// Basic provisioning: a Mosquitto CR turns into a running broker that speaks
// MQTT, and deleting the CR takes everything it owns with it.
//
// The reachability assertion is what separates this tier from integration. The
// readiness probe is a TCP connect, so a broker that accepts connections and then
// rejects every CONNECT -- a configuration file it will not load, a persistence
// directory it cannot write -- still reports Ready. Only a real MQTT session says
// otherwise, and mosquitto_pub / mosquitto_sub ship in the same image the broker
// runs (their presence is asserted in test/imagetools).

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/guided-traffic/mosquitto-operator/test/testimages"
)

// probeTopic is the topic the reachability check publishes to.
const probeTopic = "e2e/probe"

// garbageCollectionTimeout is how long the owned objects are given to disappear
// after the CR is deleted. Nothing about it is instant: the API server's garbage
// collector works off its own graph, one level at a time.
const garbageCollectionTimeout = 2 * time.Minute

// TestE2E_Mosquitto_ProvisionsAReachableBroker is the baseline the whole suite
// stands on: the operator creates the four objects, the broker comes up, it
// answers MQTT, and deleting the CR cleans up.
func TestE2E_Mosquitto_ProvisionsAReachableBroker(t *testing.T) {
	t.Parallel()
	tc := newTestClients(t)

	ns := "e2e-provisioning"
	cleanup := tc.createNamespace(t, ns)
	defer cleanup()

	name := "broker"
	image := testimages.Default()
	t.Logf("Creating a single-replica Mosquitto CR on %s", image)
	tc.createMosquitto(t, ns, buildMosquittoObject(name, ns, map[string]interface{}{
		"replicas": int64(1),
		"image":    image,
	}))
	defer tc.deleteMosquitto(t, ns, name)

	tc.waitForStatefulSetReady(t, ns, name, 1)
	tc.waitForMosquittoPhase(t, ns, name, "Ready")

	t.Run("the status mirrors the StatefulSet", func(t *testing.T) {
		status := tc.getMosquittoStatus(t, ns, name)
		assert.Equal(t, int64(1), status["readyReplicas"])

		conditions, ok := status["conditions"].([]interface{})
		require.True(t, ok, "status.conditions is missing: %v", status)
		require.NotEmpty(t, conditions)

		ready := false
		for _, raw := range conditions {
			cond, ok := raw.(map[string]interface{})
			if ok && cond["type"] == "Ready" && cond["status"] == "True" {
				ready = true
			}
		}
		assert.True(t, ready, "a Ready phase must come with a Ready=True condition: %v", conditions)
	})

	t.Run("the StatefulSet runs the requested image on the plain MQTT port", func(t *testing.T) {
		sts := tc.getStatefulSet(t, ns, name)
		assertLabelExists(t, sts.Labels, nameLabel, "mosquitto")
		assertLabelExists(t, sts.Labels, instanceLabel, name)
		assertLabelExists(t, sts.Labels, managedByLabel, managedByLabelValue)

		require.Len(t, sts.Spec.Template.Spec.Containers, 1, "the broker pod runs one container")
		container := sts.Spec.Template.Spec.Containers[0]
		assert.Equal(t, image, container.Image)
		require.Len(t, container.Ports, 1, "without TLS there is exactly one listener")
		assert.Equal(t, "mqtt", container.Ports[0].Name)
		assert.Equal(t, int32(1883), container.Ports[0].ContainerPort)
	})

	t.Run("the generated configuration is a plain MQTT listener", func(t *testing.T) {
		conf := tc.getConfigMap(t, ns, name+"-config").Data["mosquitto.conf"]
		require.NotEmpty(t, conf, "the ConfigMap carries no mosquitto.conf")
		assert.Contains(t, conf, "listener 1883")
		assert.Contains(t, conf, "persistence_location /mosquitto/data/")
		assert.NotContains(t, conf, "certfile", "no TLS was requested")
	})

	t.Run("both Services expose the broker", func(t *testing.T) {
		client := tc.getService(t, ns, name)
		require.Len(t, client.Spec.Ports, 1)
		assert.Equal(t, "mqtt", client.Spec.Ports[0].Name)
		assert.Equal(t, int32(1883), client.Spec.Ports[0].Port)
		assert.NotEqual(t, corev1.ClusterIPNone, client.Spec.ClusterIP,
			"the client Service load-balances and therefore needs a ClusterIP")

		headless := tc.getService(t, ns, name+"-headless")
		assert.Equal(t, corev1.ClusterIPNone, headless.Spec.ClusterIP)

		tc.waitForServiceEndpoints(t, ns, name)
	})

	t.Run("the broker answers MQTT", func(t *testing.T) {
		pod := name + "-0"
		payload := "provisioned"

		// Retained, so the subscriber gets it on subscribe and the two commands
		// need no overlap in time -- one kubectl exec each, no shell, no
		// background job.
		tc.podExec(t, ns, pod,
			"mosquitto_pub", "-h", "127.0.0.1", "-p", "1883",
			"-q", "1", "-r", "-t", probeTopic, "-m", payload)

		received := tc.podExec(t, ns, pod,
			"mosquitto_sub", "-h", "127.0.0.1", "-p", "1883",
			"-q", "1", "-t", probeTopic, "-C", "1", "-W", "15")

		assert.Equal(t, payload, received,
			"the broker accepted the connection but did not deliver the retained message")
	})

	t.Run("deleting the CR removes everything it owns", func(t *testing.T) {
		tc.deleteMosquitto(t, ns, name)
		tc.waitForMosquittoDeleted(t, ns, name)

		tc.waitForOwnedObjectsGone(t, ns, name)
	})
}

// waitForOwnedObjectsGone waits until the StatefulSet, both Services and the
// ConfigMap of one Mosquitto are collected. They carry owner references and
// nothing else deletes them, so their disappearance is the proof the references
// were set.
func (tc *testClients) waitForOwnedObjectsGone(t *testing.T, namespace, name string) {
	t.Helper()

	err := wait.PollUntilContextTimeout(context.Background(), pollInterval, garbageCollectionTimeout, true,
		func(ctx context.Context) (bool, error) {
			_, err := tc.kube.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
			if !apierrors.IsNotFound(err) {
				return false, nil
			}
			for _, svc := range []string{name, name + "-headless"} {
				if _, err := tc.kube.CoreV1().Services(namespace).Get(ctx, svc, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
					return false, nil
				}
			}
			_, err = tc.kube.CoreV1().ConfigMaps(namespace).Get(ctx, name+"-config", metav1.GetOptions{})
			return apierrors.IsNotFound(err), nil
		})
	require.NoError(t, err,
		"the objects owned by Mosquitto %s/%s were not garbage collected", namespace, name)
}
