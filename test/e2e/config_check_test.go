//go:build e2e

package e2e

// The config-check init container runs the broker binary of the pod's own image
// in --test-config mode before the broker starts (ADR 0007 D10). A directive
// the broker does not know stops the pod there, with the broker's own message,
// file and line - not in a crash loop of the broker container.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/guided-traffic/mosquitto-operator/test/testimages"
)

const (
	configCheckContainer = "config-check"
	configPath           = "/mosquitto/config/mosquitto.conf"
	misspelledDirective  = "max_queued_mesages"
)

// TestE2E_ConfigCheck_StopsATypoBeforeTheBroker: a misspelled directive in
// spec.config leaves the broker container unstarted, and the init container's
// log names the directive and the line of the generated file it sits on.
func TestE2E_ConfigCheck_StopsATypoBeforeTheBroker(t *testing.T) {
	t.Parallel()
	tc := newTestClients(t)

	ns := "e2e-config-check"
	cleanup := tc.createNamespace(t, ns)
	defer cleanup()

	name := "typo"
	tc.createMosquitto(t, ns, buildMosquittoObject(name, ns, map[string]interface{}{
		"replicas": int64(1),
		"image":    testimages.Default(),
		"config":   misspelledDirective + " 100\n",
	}))
	defer tc.deleteMosquitto(t, ns, name)

	// The line the directive lands on in the generated file, read back from the
	// ConfigMap rather than counted here, so a change to the generated header
	// does not break this test.
	var line int
	require.Eventually(t, func() bool {
		cm, err := tc.kube.CoreV1().ConfigMaps(ns).Get(context.Background(), name+"-config", metav1.GetOptions{})
		if err != nil {
			return false
		}
		for i, l := range strings.Split(cm.Data["mosquitto.conf"], "\n") {
			if strings.HasPrefix(l, misspelledDirective) {
				line = i + 1
			}
		}
		return line > 0
	}, testTimeout, pollInterval, "the ConfigMap never carried the directive")

	podName := name + "-0"
	var pod *corev1.Pod
	err := wait.PollUntilContextTimeout(context.Background(), pollInterval, testTimeout, true,
		func(ctx context.Context) (bool, error) {
			p, err := tc.kube.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
			if err != nil {
				return false, nil
			}
			pod = p
			for _, status := range p.Status.InitContainerStatuses {
				if status.Name == configCheckContainer && status.RestartCount > 0 {
					return true, nil
				}
			}
			return false, nil
		})
	require.NoError(t, err, "the config-check init container never failed for %s/%s", ns, podName)

	for _, status := range pod.Status.ContainerStatuses {
		assert.Nil(t, status.State.Running, "the broker container must not start on a configuration it rejects")
		assert.Zero(t, status.RestartCount, "the failure belongs to the init container, not to a broker crash loop")
	}

	logs, err := tc.kube.CoreV1().Pods(ns).GetLogs(podName, &corev1.PodLogOptions{
		Container: configCheckContainer, Previous: true,
	}).DoRaw(context.Background())
	require.NoError(t, err, "reading the log of the failed config-check attempt")
	assert.Contains(t, string(logs), fmt.Sprintf("Error: Unknown configuration variable '%s'.", misspelledDirective))
	assert.Contains(t, string(logs), fmt.Sprintf("Error found at %s:%d.", configPath, line))
}
