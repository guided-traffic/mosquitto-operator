//go:build e2e

package e2e

// spec.podLabels and spec.podAnnotations reach the broker pods through a roll,
// and a key removed from the CR leaves them (ADR 0012 D5, ADR 0009 D9). envtest
// starts no pod, so only this tier sees the labels arrive where a NetworkPolicy
// or a scraper would select on them.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"

	"github.com/guided-traffic/mosquitto-operator/test/testimages"
)

// updateMosquittoSpec sets fields of a Mosquitto's spec, retrying on a
// conflict: the operator writes the status between a read and the update.
func (tc *testClients) updateMosquittoSpec(t *testing.T, namespace, name string, fields map[string]interface{}) {
	t.Helper()
	ctx := context.Background()
	client := tc.dynamic.Resource(mosquittoGVR).Namespace(namespace)

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		m, err := client.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		for field, value := range fields {
			if err := unstructured.SetNestedField(m.Object, value, "spec", field); err != nil {
				return err
			}
		}
		_, err = client.Update(ctx, m, metav1.UpdateOptions{})
		return err
	})
	require.NoError(t, err, "updating the spec of %s/%s", namespace, name)
}

// waitForBrokerPod waits until the single broker pod of a Mosquitto is ready and
// satisfies accept, and returns its labels and annotations.
func (tc *testClients) waitForBrokerPod(t *testing.T, namespace, name, what string,
	accept func(labels, annotations map[string]string) bool) {
	t.Helper()

	err := wait.PollUntilContextTimeout(context.Background(), pollInterval, testTimeout, true,
		func(context.Context) (bool, error) {
			for _, pod := range tc.listBrokerPods(t, namespace, name) {
				ready := false
				for _, cond := range pod.Status.Conditions {
					ready = ready || (cond.Type == "Ready" && cond.Status == "True")
				}
				if ready && pod.DeletionTimestamp == nil && accept(pod.Labels, pod.Annotations) {
					return true, nil
				}
			}
			return false, nil
		})
	require.NoError(t, err, "no ready broker pod of %s/%s %s", namespace, name, what)
}

// TestE2E_PodMetadata_ReachesAndLeavesThePods: a label and an annotation set on
// the CR are on the pod; replacing them rolls the pod, the new key arrives and
// the removed one is gone.
func TestE2E_PodMetadata_ReachesAndLeavesThePods(t *testing.T) {
	t.Parallel()
	tc := newTestClients(t)

	ns := "e2e-pod-metadata"
	cleanup := tc.createNamespace(t, ns)
	defer cleanup()

	name := "broker"
	tc.createMosquitto(t, ns, buildMosquittoObject(name, ns, map[string]interface{}{
		"replicas":       int64(1),
		"image":          testimages.Default(),
		"podLabels":      map[string]interface{}{"e2e.example.com/team": "iot"},
		"podAnnotations": map[string]interface{}{"e2e.example.com/note": "first"},
	}))
	defer tc.deleteMosquitto(t, ns, name)

	tc.waitForBrokerPod(t, ns, name, "carries the label and the annotation of the CR",
		func(labels, annotations map[string]string) bool {
			return labels["e2e.example.com/team"] == "iot" && annotations["e2e.example.com/note"] == "first" &&
				labels[instanceLabel] == name
		})

	tc.updateMosquittoSpec(t, ns, name, map[string]interface{}{
		"podLabels":      map[string]interface{}{"e2e.example.com/zone": "a"},
		"podAnnotations": map[string]interface{}{"e2e.example.com/note": "second"},
	})

	tc.waitForBrokerPod(t, ns, name, "rolled to the new label, without the removed one",
		func(labels, annotations map[string]string) bool {
			_, stale := labels["e2e.example.com/team"]
			return labels["e2e.example.com/zone"] == "a" && !stale && annotations["e2e.example.com/note"] == "second"
		})
}
