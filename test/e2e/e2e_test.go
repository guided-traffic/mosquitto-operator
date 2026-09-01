//go:build e2e

// Package e2e runs the operator end to end against a real Kubernetes cluster
// (Kind in CI) with the operator installed from the Helm chart.
//
// The suite deliberately imports nothing from internal/: it addresses the API
// group, the object names, the labels and the ports as literals, the way a user
// does. A helper that imported the builder would agree with a renamed constant by
// construction and stop being a test of the contract the operator publishes.
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// testTimeout is the maximum time to wait for a resource to reach a state. It
// covers a cold image pull on a loaded Kind node, which is the slowest step of
// every provisioning test.
const testTimeout = 5 * time.Minute

// pollInterval is the interval between polling attempts.
const pollInterval = 2 * time.Second

// The API surface under test, as a user addresses it.
const (
	mosquittoAPIVersion = "mko.gtrfc.com/v1"
	mosquittoKind       = "Mosquitto"

	// managedByLabelValue is the value the operator stamps on everything it
	// creates; together with the instance label it selects the pods of one
	// Mosquitto.
	managedByLabelValue = "mosquitto-operator"

	instanceLabel  = "app.kubernetes.io/instance"
	nameLabel      = "app.kubernetes.io/name"
	managedByLabel = "app.kubernetes.io/managed-by"
)

// mosquittoGVR is the GroupVersionResource of the Mosquitto CRD.
var mosquittoGVR = schema.GroupVersionResource{
	Group:    "mko.gtrfc.com",
	Version:  "v1",
	Resource: "mosquittoes",
}

// testClients holds the shared Kubernetes clients of the suite.
type testClients struct {
	kube    kubernetes.Interface
	dynamic dynamic.Interface
}

// newTestClients builds Kubernetes clients from the current kubeconfig.
func newTestClients(t *testing.T) *testClients {
	t.Helper()

	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, err := os.UserHomeDir()
		require.NoError(t, err)
		kubeconfig = home + "/.kube/config"
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	require.NoError(t, err, "Failed to build kubeconfig")

	// The client-go default of 5 QPS / 10 burst is too conservative for several
	// parallel tests each running a long polling loop; they exhaust the rate
	// limiter and fail with "client rate limiter Wait returned an error".
	config.QPS = 50
	config.Burst = 100

	kubeClient, err := kubernetes.NewForConfig(config)
	require.NoError(t, err, "Failed to create the kubernetes client")

	dynClient, err := dynamic.NewForConfig(config)
	require.NoError(t, err, "Failed to create the dynamic client")

	return &testClients{kube: kubeClient, dynamic: dynClient}
}

// createNamespace creates a test namespace and returns its cleanup function.
func (tc *testClients) createNamespace(t *testing.T, name string) func() {
	t.Helper()
	ctx := context.Background()

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}

	_, err := tc.kube.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		// A leftover from an interrupted local run: delete it and wait, so the
		// test starts from an empty namespace rather than adopting whatever is
		// still in there.
		_ = tc.kube.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
		require.Eventually(t, func() bool {
			_, err := tc.kube.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
			return apierrors.IsNotFound(err)
		}, 60*time.Second, time.Second, "Namespace %s did not get deleted", name)
		_, err = tc.kube.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	}
	require.NoError(t, err, "Failed to create namespace %s", name)

	return func() {
		// A failed test keeps its namespace. The workflow collects pod logs and
		// events after the suite has finished, so deleting here would remove
		// exactly the evidence that collection exists for -- the Events outlive
		// the pods and are what names a kill, an eviction or a failing probe. The
		// cluster is thrown away at the end of the job either way.
		if t.Failed() {
			t.Logf("Test failed: keeping namespace %s for post-run collection", name)
			return
		}
		_ = tc.kube.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
	}
}

// buildMosquittoObject constructs an unstructured Mosquitto CR.
func buildMosquittoObject(name, namespace string, spec map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": mosquittoAPIVersion,
			"kind":       mosquittoKind,
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": spec,
		},
	}
}

// createMosquitto creates a Mosquitto CR.
func (tc *testClients) createMosquitto(t *testing.T, namespace string, m *unstructured.Unstructured) {
	t.Helper()

	_, err := tc.dynamic.Resource(mosquittoGVR).Namespace(namespace).Create(
		context.Background(), m, metav1.CreateOptions{})
	require.NoError(t, err, "Failed to create the Mosquitto CR")
}

// deleteMosquitto deletes a Mosquitto CR. It is called from a defer, so an
// already-deleted resource is not an error.
func (tc *testClients) deleteMosquitto(t *testing.T, namespace, name string) {
	t.Helper()

	err := tc.dynamic.Resource(mosquittoGVR).Namespace(namespace).Delete(
		context.Background(), name, metav1.DeleteOptions{})
	if !apierrors.IsNotFound(err) {
		require.NoError(t, err, "Failed to delete the Mosquitto CR %s/%s", namespace, name)
	}
}

// waitForMosquittoDeleted waits until a Mosquitto CR is gone from the API server.
func (tc *testClients) waitForMosquittoDeleted(t *testing.T, namespace, name string) {
	t.Helper()

	err := wait.PollUntilContextTimeout(context.Background(), pollInterval, testTimeout, true,
		func(ctx context.Context) (bool, error) {
			_, err := tc.dynamic.Resource(mosquittoGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		})
	require.NoError(t, err, "Mosquitto CR %s/%s was not deleted", namespace, name)
}

// waitForMosquittoPhase waits until a Mosquitto CR reports the expected phase.
func (tc *testClients) waitForMosquittoPhase(t *testing.T, namespace, name, expected string) {
	t.Helper()

	err := wait.PollUntilContextTimeout(context.Background(), pollInterval, testTimeout, true,
		func(ctx context.Context) (bool, error) {
			m, err := tc.dynamic.Resource(mosquittoGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			phase, found, err := unstructured.NestedString(m.Object, "status", "phase")
			if err != nil || !found {
				return false, nil
			}
			ready, _, _ := unstructured.NestedInt64(m.Object, "status", "readyReplicas")
			t.Logf("Mosquitto %s phase: %s (want %s), readyReplicas: %d", name, phase, expected, ready)
			return phase == expected, nil
		})
	require.NoError(t, err, "Mosquitto %s/%s did not reach phase %s", namespace, name, expected)
}

// getMosquittoStatus returns the status of a Mosquitto CR.
func (tc *testClients) getMosquittoStatus(t *testing.T, namespace, name string) map[string]interface{} {
	t.Helper()

	m, err := tc.dynamic.Resource(mosquittoGVR).Namespace(namespace).Get(
		context.Background(), name, metav1.GetOptions{})
	require.NoError(t, err, "Failed to get the Mosquitto CR %s/%s", namespace, name)

	status, found, err := unstructured.NestedMap(m.Object, "status")
	require.NoError(t, err)
	require.True(t, found, "the Mosquitto CR carries no status")
	return status
}

// waitForStatefulSetReady waits until a StatefulSet reports the expected number
// of ready replicas.
func (tc *testClients) waitForStatefulSetReady(t *testing.T, namespace, name string, replicas int32) {
	t.Helper()

	err := wait.PollUntilContextTimeout(context.Background(), pollInterval, testTimeout, true,
		func(ctx context.Context) (bool, error) {
			sts, err := tc.kube.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) {
					return false, nil
				}
				return false, err
			}
			t.Logf("StatefulSet %s: ready=%d/%d", name, sts.Status.ReadyReplicas, replicas)
			return sts.Status.ReadyReplicas == replicas, nil
		})
	require.NoError(t, err, "StatefulSet %s/%s did not become ready with %d replicas", namespace, name, replicas)
}

// getStatefulSet retrieves a StatefulSet.
func (tc *testClients) getStatefulSet(t *testing.T, namespace, name string) *appsv1.StatefulSet {
	t.Helper()

	sts, err := tc.kube.AppsV1().StatefulSets(namespace).Get(
		context.Background(), name, metav1.GetOptions{})
	require.NoError(t, err, "Failed to get StatefulSet %s/%s", namespace, name)
	return sts
}

// getService retrieves a Service.
func (tc *testClients) getService(t *testing.T, namespace, name string) *corev1.Service {
	t.Helper()

	svc, err := tc.kube.CoreV1().Services(namespace).Get(
		context.Background(), name, metav1.GetOptions{})
	require.NoError(t, err, "Failed to get Service %s/%s", namespace, name)
	return svc
}

// getConfigMap retrieves a ConfigMap.
func (tc *testClients) getConfigMap(t *testing.T, namespace, name string) *corev1.ConfigMap {
	t.Helper()

	cm, err := tc.kube.CoreV1().ConfigMaps(namespace).Get(
		context.Background(), name, metav1.GetOptions{})
	require.NoError(t, err, "Failed to get ConfigMap %s/%s", namespace, name)
	return cm
}

// waitForServiceEndpoints waits until a Service has at least one ready address.
// A Service with endpoints is the statement a client cares about; a Service
// object alone routes nowhere.
func (tc *testClients) waitForServiceEndpoints(t *testing.T, namespace, name string) {
	t.Helper()

	err := wait.PollUntilContextTimeout(context.Background(), pollInterval, testTimeout, true,
		func(ctx context.Context) (bool, error) {
			slices, err := tc.kube.DiscoveryV1().EndpointSlices(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: "kubernetes.io/service-name=" + name,
			})
			if err != nil {
				return false, err
			}
			for _, slice := range slices.Items {
				for _, ep := range slice.Endpoints {
					if ep.Conditions.Ready == nil || *ep.Conditions.Ready {
						return true, nil
					}
				}
			}
			return false, nil
		})
	require.NoError(t, err, "Service %s/%s did not get endpoints", namespace, name)
}

// listBrokerPods lists every pod of one Mosquitto, regardless of phase -- the
// Pending pods of a spread that cannot be satisfied matter to the caller.
func (tc *testClients) listBrokerPods(t *testing.T, namespace, instance string) []corev1.Pod {
	t.Helper()

	pods, err := tc.kube.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: instanceLabel + "=" + instance + "," + managedByLabel + "=" + managedByLabelValue,
	})
	require.NoError(t, err, "Failed to list broker pods in %s", namespace)
	return pods.Items
}

// assertLabelExists checks one label on an object's metadata.
func assertLabelExists(t *testing.T, labels map[string]string, key, expected string) {
	t.Helper()

	value, ok := labels[key]
	assert.True(t, ok, "Label %s not found", key)
	assert.Equal(t, expected, value, "Label %s has the wrong value", key)
}

// execAttempts is how often podExec retries. A pod that has just passed its
// readiness probe can still refuse an exec while the kubelet finishes wiring the
// container up.
const execAttempts = 5

// execTimeout bounds one kubectl exec attempt.
const execTimeout = 30 * time.Second

// podExec runs a command inside a broker pod and returns its stdout, retrying
// transient failures.
func (tc *testClients) podExec(t *testing.T, namespace, podName string, command ...string) string {
	t.Helper()

	var lastErr error
	for attempt := 1; attempt <= execAttempts; attempt++ {
		if attempt > 1 {
			delay := time.Duration(attempt) * time.Second
			t.Logf("Retrying exec in %s/%s (attempt %d/%d, backoff %v)", namespace, podName, attempt, execAttempts, delay)
			time.Sleep(delay)
		}
		out, err := tc.podExecOnce(namespace, podName, command...)
		if err == nil {
			return out
		}
		lastErr = err
	}

	require.NoError(t, lastErr, "exec failed after %d attempts in %s/%s", execAttempts, namespace, podName)
	return "" // unreachable
}

// podExecOnce runs one kubectl exec and keeps stdout and stderr apart, so the
// answer a client tool prints is never mixed with the diagnostics kubectl writes.
func (tc *testClients) podExecOnce(namespace, podName string, command ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()

	args := append([]string{"exec", podName, "-n", namespace, "--"}, command...)
	cmd := exec.CommandContext(ctx, "kubectl", args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(stdout.String()),
			fmt.Errorf("kubectl exec %v in %s/%s: %w (stderr: %s)",
				command, namespace, podName, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}
