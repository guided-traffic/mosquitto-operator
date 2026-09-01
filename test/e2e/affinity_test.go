//go:build e2e

package e2e

// Tests in this file cover the pod anti-affinity the operator renders into the
// broker pod template.
//
// The default is off -- installing or upgrading the operator must not change how
// existing brokers are scheduled -- so spreading is an explicit opt-in to soft or
// hard. The off-default and soft assertions hold on any cluster shape. The
// hard-mode spread assertion needs at least three schedulable nodes (`make
// kind-create` locally, the multi-node leg in .github/workflows/release.yml) and
// skips otherwise -- unless E2E_REQUIRE_MULTI_NODE=true, which that leg sets, so
// a cluster that came up smaller fails instead of skipping.

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/guided-traffic/mosquitto-operator/test/testimages"
)

// hostnameTopologyKey is the spread domain the operator emits: one broker pod
// per node.
const hostnameTopologyKey = "kubernetes.io/hostname"

// multiNodeRequiredEnv turns the "not enough nodes" skip below into a failure. CI
// sets it on the multi-node leg (.github/workflows/release.yml): a cluster that
// came up smaller than requested would otherwise skip the one assertion that leg
// exists for, and a skip reads as green.
const multiNodeRequiredEnv = "E2E_REQUIRE_MULTI_NODE"

// TestE2E_AntiAffinity_OffByDefault pins the default: a CR that says nothing
// about anti-affinity gets no affinity block at all, so the operator changes
// nothing about how an existing broker is scheduled.
func TestE2E_AntiAffinity_OffByDefault(t *testing.T) {
	t.Parallel()
	tc := newTestClients(t)

	ns := "e2e-antiaffinity-off"
	cleanup := tc.createNamespace(t, ns)
	defer cleanup()

	name := "aa-off"
	t.Log("Creating a Mosquitto CR without an antiAffinity field")
	tc.createMosquitto(t, ns, buildMosquittoObject(name, ns, map[string]interface{}{
		"replicas": int64(1),
		"image":    testimages.Default(),
	}))
	defer tc.deleteMosquitto(t, ns, name)

	tc.waitForStatefulSetReady(t, ns, name, 1)

	assert.Nil(t, tc.getStatefulSet(t, ns, name).Spec.Template.Spec.Affinity,
		"StatefulSet %s must carry no affinity without an explicit opt-in", name)
}

// TestE2E_AntiAffinity_SoftWhenRequested is the soft half: the operator emits a
// preferred term, and -- because a preference never blocks scheduling -- three
// replicas become ready even on a cluster that cannot satisfy the spread.
func TestE2E_AntiAffinity_SoftWhenRequested(t *testing.T) {
	t.Parallel()
	tc := newTestClients(t)

	ns := "e2e-antiaffinity-soft"
	cleanup := tc.createNamespace(t, ns)
	defer cleanup()

	name := "aa-soft"
	t.Log("Creating a Mosquitto CR with antiAffinity: soft")
	tc.createMosquitto(t, ns, buildMosquittoObject(name, ns, map[string]interface{}{
		"replicas":     int64(3),
		"image":        testimages.Default(),
		"antiAffinity": "soft",
	}))
	defer tc.deleteMosquitto(t, ns, name)

	// The wait is itself the assertion that soft does not block: on the
	// single-node leg all three pods have to land on the one node anyway.
	tc.waitForStatefulSetReady(t, ns, name, 3)

	term := requirePreferredAntiAffinity(t, tc.getStatefulSet(t, ns, name))
	assert.Equal(t, int32(100), term.Weight, "the spread must be the strongest preference")
	assert.Equal(t, hostnameTopologyKey, term.PodAffinityTerm.TopologyKey)
	assertBrokerSelector(t, term.PodAffinityTerm, name)
}

// TestE2E_AntiAffinity_HardSpreadsAcrossNodes is the hard half: with
// antiAffinity: hard the three broker pods land on three distinct nodes. Needs a
// cluster with at least three schedulable nodes.
//
// The name is greppable on purpose: the multi-node CI leg runs with a -run filter
// and then proves in the log that this test actually executed, so a rename that
// stops matching the filter fails the leg instead of turning it green.
func TestE2E_AntiAffinity_HardSpreadsAcrossNodes(t *testing.T) {
	t.Parallel()
	tc := newTestClients(t)

	tc.requireThreeSchedulableNodes(t)

	ns := "e2e-antiaffinity-hard"
	cleanup := tc.createNamespace(t, ns)
	defer cleanup()

	name := "aa-hard"
	t.Log("Creating a Mosquitto CR with antiAffinity: hard")
	tc.createMosquitto(t, ns, buildMosquittoObject(name, ns, map[string]interface{}{
		"replicas":     int64(3),
		"image":        testimages.Default(),
		"antiAffinity": "hard",
	}))
	defer tc.deleteMosquitto(t, ns, name)

	tc.waitForStatefulSetReady(t, ns, name, 3)

	term := requireRequiredAntiAffinity(t, tc.getStatefulSet(t, ns, name))
	assert.Equal(t, hostnameTopologyKey, term.TopologyKey)
	assertBrokerSelector(t, term, name)

	assertDistinctNodes(t, tc.podNodeNames(t, ns, name))
}

// --- helpers ---

// requirePreferredAntiAffinity returns the single preferred (soft) anti-affinity
// term of a StatefulSet's pod template and fails if a required one is present.
func requirePreferredAntiAffinity(t *testing.T, sts *appsv1.StatefulSet) corev1.WeightedPodAffinityTerm {
	t.Helper()

	affinity := sts.Spec.Template.Spec.Affinity
	require.NotNil(t, affinity, "StatefulSet %s has no affinity", sts.Name)
	require.NotNil(t, affinity.PodAntiAffinity, "StatefulSet %s has no pod anti-affinity", sts.Name)
	assert.Empty(t, affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution,
		"soft must never block scheduling")

	preferred := affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	require.Len(t, preferred, 1, "expected exactly one preferred anti-affinity term")
	return preferred[0]
}

// requireRequiredAntiAffinity returns the single required (hard) anti-affinity
// term of a StatefulSet's pod template.
func requireRequiredAntiAffinity(t *testing.T, sts *appsv1.StatefulSet) corev1.PodAffinityTerm {
	t.Helper()

	affinity := sts.Spec.Template.Spec.Affinity
	require.NotNil(t, affinity, "StatefulSet %s has no affinity", sts.Name)
	require.NotNil(t, affinity.PodAntiAffinity, "StatefulSet %s has no pod anti-affinity", sts.Name)
	assert.Empty(t, affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution,
		"hard mode must not additionally emit a preference")

	required := affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	require.Len(t, required, 1, "expected exactly one required anti-affinity term")
	return required[0]
}

// assertBrokerSelector checks that a term repels only the broker pods of the same
// Mosquitto -- never another instance in the same namespace.
func assertBrokerSelector(t *testing.T, term corev1.PodAffinityTerm, instance string) {
	t.Helper()

	require.NotNil(t, term.LabelSelector, "an anti-affinity term must carry a label selector")
	assert.Equal(t, map[string]string{
		nameLabel:      "mosquitto",
		instanceLabel:  instance,
		managedByLabel: managedByLabelValue,
	}, term.LabelSelector.MatchLabels)
}

// assertDistinctNodes requires every pod to sit on its own node.
func assertDistinctNodes(t *testing.T, nodes []string) {
	t.Helper()

	require.NotEmpty(t, nodes, "no broker pods found")
	seen := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		require.NotEmpty(t, node, "a broker pod is unscheduled: %v", nodes)
		require.False(t, seen[node], "two broker pods share node %s: %v", node, nodes)
		seen[node] = true
	}
}

// podNodeNames returns the node of every broker pod, in list order. An empty
// entry means the pod is not scheduled.
func (tc *testClients) podNodeNames(t *testing.T, namespace, instance string) []string {
	t.Helper()

	pods := tc.listBrokerPods(t, namespace, instance)
	nodes := make([]string, 0, len(pods))
	for i := range pods {
		nodes = append(nodes, pods[i].Spec.NodeName)
	}
	return nodes
}

// requireThreeSchedulableNodes skips the hard-mode spread assertion on a cluster
// too small to satisfy it -- unless multiNodeRequiredEnv says the cluster was
// built for exactly this test, in which case a small cluster is the defect.
func (tc *testClients) requireThreeSchedulableNodes(t *testing.T) {
	t.Helper()

	nodes := tc.schedulableNodeCount(t)
	if nodes >= 3 {
		return
	}
	if os.Getenv(multiNodeRequiredEnv) == "true" {
		t.Fatalf("%s=true but the cluster has only %d schedulable nodes: hard-mode spread needs at least 3",
			multiNodeRequiredEnv, nodes)
	}
	t.Skipf("hard-mode spread needs at least 3 schedulable nodes, cluster has %d", nodes)
}

// schedulableNodeCount counts the nodes a pod without tolerations could actually
// land on: Ready, not cordoned and not carrying a NoSchedule taint. The taint
// check matters on multi-node Kind, where the control-plane node is Ready but
// tainted and would otherwise inflate the count past the skip threshold.
func (tc *testClients) schedulableNodeCount(t *testing.T) int {
	t.Helper()

	nodes, err := tc.kube.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err, "Failed to list nodes")

	count := 0
	for i := range nodes.Items {
		node := nodes.Items[i]
		if !node.Spec.Unschedulable && isNodeReady(node) && !hasNoScheduleTaint(node) {
			count++
		}
	}
	return count
}

// hasNoScheduleTaint reports whether a node repels pods that carry no toleration.
func hasNoScheduleTaint(node corev1.Node) bool {
	for _, taint := range node.Spec.Taints {
		if taint.Effect == corev1.TaintEffectNoSchedule || taint.Effect == corev1.TaintEffectNoExecute {
			return true
		}
	}
	return false
}

// isNodeReady reports whether a node carries a Ready=True condition.
func isNodeReady(node corev1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}
