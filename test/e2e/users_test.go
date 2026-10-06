//go:build e2e

package e2e

// Users, permissions and a broker that requires a login (ADR 0013, ADR 0014,
// ADR 0008 D13-D15). This is the tier where a credential reaches a running
// broker: the kubelet refreshes the mounted Secret, the reloader copies it and
// signals the broker, and an MQTT client finds out. Nothing cheaper starts a pod.

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/guided-traffic/mosquitto-operator/test/testimages"
)

var mosquittoUserGVR = schema.GroupVersionResource{Group: "mko.gtrfc.com", Version: "v1", Resource: "mosquittousers"}

// reloadTimeout bounds how long a credential change may take to reach a
// running broker: the kubelet's refresh of the Secret volume, measured at 69 to
// 84 seconds on an idle Kind node (docs/developer/broker-behaviour.md M23), plus
// the reloader's poll.
const reloadTimeout = 4 * time.Minute

// acl is one ACL entry of a MosquittoUser, as the API spells it.
func acl(topic, access string) map[string]interface{} {
	return map[string]interface{}{"topic": topic, "access": access}
}

// createCredentials writes a kubernetes.io/basic-auth Secret: the keys a
// MosquittoUser reads by default and a client application reads too.
func (tc *testClients) createCredentials(t *testing.T, namespace, name, username, password string) {
	t.Helper()
	_, err := tc.kube.CoreV1().Secrets(namespace).Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretTypeBasicAuth,
		StringData: map[string]string{"username": username, "password": password},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "creating Secret %s/%s", namespace, name)
}

// setPassword replaces the password in a credentials Secret, as a GitOps
// change to the Secret alone would.
func (tc *testClients) setPassword(t *testing.T, namespace, name, password string) {
	t.Helper()
	ctx := context.Background()
	secret, err := tc.kube.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	secret.Data["password"] = []byte(password)
	_, err = tc.kube.CoreV1().Secrets(namespace).Update(ctx, secret, metav1.UpdateOptions{})
	require.NoError(t, err)
}

// createUser writes a MosquittoUser naming a broker and a credentials Secret.
func (tc *testClients) createUser(t *testing.T, namespace, name, broker, secret string, acls ...map[string]interface{}) {
	t.Helper()
	entries := make([]interface{}, 0, len(acls))
	for _, a := range acls {
		entries = append(entries, a)
	}
	_, err := tc.dynamic.Resource(mosquittoUserGVR).Namespace(namespace).Create(context.Background(),
		&unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": mosquittoAPIVersion,
			"kind":       "MosquittoUser",
			"metadata":   map[string]interface{}{"name": name, "namespace": namespace},
			"spec": map[string]interface{}{
				"brokerRef":         map[string]interface{}{"name": broker},
				"credentialsSecret": map[string]interface{}{"name": secret},
				"acls":              entries,
			},
		}}, metav1.CreateOptions{})
	require.NoError(t, err, "creating MosquittoUser %s/%s", namespace, name)
}

// waitForUserReady waits until a MosquittoUser reports Ready=True.
func (tc *testClients) waitForUserReady(t *testing.T, namespace, name string) {
	t.Helper()
	tc.waitForUserCondition(t, namespace, name, "True", "")
}

// waitForUserReason waits until a MosquittoUser reports Ready=False with reason.
func (tc *testClients) waitForUserReason(t *testing.T, namespace, name, reason string) {
	t.Helper()
	tc.waitForUserCondition(t, namespace, name, "False", reason)
}

// waitForUserCondition waits for the Ready condition of a MosquittoUser to have
// status, and reason unless reason is empty.
func (tc *testClients) waitForUserCondition(t *testing.T, namespace, name, status, reason string) {
	t.Helper()
	err := wait.PollUntilContextTimeout(context.Background(), pollInterval, testTimeout, true,
		func(ctx context.Context) (bool, error) {
			u, err := tc.dynamic.Resource(mosquittoUserGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, nil
			}
			conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
			for _, raw := range conditions {
				cond, ok := raw.(map[string]interface{})
				if ok && cond["type"] == "Ready" && cond["status"] == status && (reason == "" || cond["reason"] == reason) {
					return true, nil
				}
			}
			return false, nil
		})
	require.NoError(t, err, "MosquittoUser %s/%s never reported Ready=%s %s", namespace, name, status, reason)
}

// mqtt runs one MQTT client tool in the broker container of a pod, once, and
// returns its output and exit code. Unlike podExec it does not retry: a refused
// login is an answer here, not a transient failure.
func (tc *testClients) mqtt(namespace, pod string, command ...string) (string, int) {
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	args := append([]string{"exec", pod, "-n", namespace, "-c", "mosquitto", "--"}, command...)
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		code = -1
	}
	return strings.TrimSpace(out.String()), code
}

// publish publishes one retained message as a user ("" for anonymous).
func (tc *testClients) publish(namespace, pod, user, password, topic, payload string) (string, int) {
	command := []string{"mosquitto_pub", "-h", "127.0.0.1", "-p", "1883", "-q", "1", "-r", "-t", topic, "-m", payload}
	if user != "" {
		command = append(command, "-u", user, "-P", password)
	}
	return tc.mqtt(namespace, pod, command...)
}

// receive reads one retained message as a user, or times out.
func (tc *testClients) receive(namespace, pod, user, password, topic string) (string, int) {
	return tc.mqtt(namespace, pod, "mosquitto_sub", "-h", "127.0.0.1", "-p", "1883", "-q", "1",
		"-u", user, "-P", password, "-t", topic, "-C", "1", "-W", "5")
}

// eventuallyAccepted polls until a user's publish is accepted, which happens
// once the kubelet has refreshed the credentials and the reloader has signalled.
func (tc *testClients) eventuallyAccepted(t *testing.T, namespace, pod, user, password, topic string) {
	t.Helper()
	var out string
	var code int
	err := wait.PollUntilContextTimeout(context.Background(), 5*time.Second, reloadTimeout, true,
		func(context.Context) (bool, error) {
			out, code = tc.publish(namespace, pod, user, password, topic, "probe")
			return code == 0, nil
		})
	require.NoError(t, err, "%s was never accepted: rc=%d %s", user, code, out)
}

// eventuallyRefused polls until a login is refused with "not authorised" (5).
func (tc *testClients) eventuallyRefused(t *testing.T, namespace, pod, user, password string) {
	t.Helper()
	var out string
	var code int
	err := wait.PollUntilContextTimeout(context.Background(), 5*time.Second, reloadTimeout, true,
		func(context.Context) (bool, error) {
			out, code = tc.publish(namespace, pod, user, password, "e2e/refused", "x")
			return code == 5, nil
		})
	require.NoError(t, err, "%s was never refused: rc=%d %s", user, code, out)
}

// brokerPodIdentity returns what a restart would change: the pod's UID and the
// broker container's restart count.
func (tc *testClients) brokerPodIdentity(t *testing.T, namespace, pod string) string {
	t.Helper()
	p, err := tc.kube.CoreV1().Pods(namespace).Get(context.Background(), pod, metav1.GetOptions{})
	require.NoError(t, err)
	restarts := int32(-1)
	for _, status := range p.Status.ContainerStatuses {
		if status.Name == "mosquitto" {
			restarts = status.RestartCount
		}
	}
	return fmt.Sprintf("%s/%d", p.UID, restarts)
}

// startSubscriber starts a long-running mosquitto_sub in the broker container
// and returns a channel that is closed when the client process ends.
func (tc *testClients) startSubscriber(t *testing.T, namespace, pod, user, password, topic string, extra ...string) (<-chan struct{}, *syncBuffer, func()) {
	t.Helper()
	return tc.startClient(t, namespace, pod, append([]string{
		"mosquitto_sub", "-h", "127.0.0.1", "-p", "1883", "-q", "1", "-u", user, "-P", password, "-t", topic}, extra...)...)
}

// startClient starts a long-running client command in the broker container.
func (tc *testClients) startClient(t *testing.T, namespace, pod string, command ...string) (<-chan struct{}, *syncBuffer, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	args := append([]string{"exec", pod, "-n", namespace, "-c", "mosquitto", "--"}, command...)
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	out := &syncBuffer{}
	cmd.Stdout = out
	cmd.Stderr = out
	require.NoError(t, cmd.Start())
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	return done, out, cancel
}

// syncBuffer is the output of a running client, read while it still writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestE2E_Users_TheBrokerFollowsItsUsers is the phase: one broker, users added,
// changed and removed while it runs, and every change reaching it without a
// restart and without an edit to any custom resource.
func TestE2E_Users_TheBrokerFollowsItsUsers(t *testing.T) {
	t.Parallel()
	tc := newTestClients(t)

	ns := "e2e-users"
	cleanup := tc.createNamespace(t, ns)
	defer cleanup()

	name := "broker"
	pod := name + "-0"
	tc.createCredentials(t, ns, "observer-mqtt", "observer", "observer-pw")
	tc.createUser(t, ns, "observer", name, "observer-mqtt", acl("#", "readwrite"))
	tc.createMosquitto(t, ns, buildMosquittoObject(name, ns, map[string]interface{}{
		"replicas": int64(1),
		"image":    testimages.Default(),
	}))
	defer tc.deleteMosquitto(t, ns, name)
	tc.waitForStatefulSetReady(t, ns, name, 1)
	tc.waitForUserReady(t, ns, "observer")
	started := tc.brokerPodIdentity(t, ns, pod)

	t.Run("an anonymous client is refused", func(t *testing.T) {
		out, code := tc.publish(ns, pod, "", "", "e2e/anonymous", "x")
		assert.Equal(t, 5, code, out)
		assert.Contains(t, out, "not authorised")
	})

	t.Run("a user created against the running broker can publish, and nothing restarted", func(t *testing.T) {
		tc.createCredentials(t, ns, "z2m-mqtt", "zigbee2mqtt", "z2m-pw")
		tc.createUser(t, ns, "zigbee2mqtt", name, "z2m-mqtt", acl("zigbee2mqtt/#", "readwrite"))
		tc.waitForUserReady(t, ns, "zigbee2mqtt")

		tc.eventuallyAccepted(t, ns, pod, "zigbee2mqtt", "z2m-pw", "zigbee2mqtt/bridge/state")
		received, code := tc.receive(ns, pod, "observer", "observer-pw", "zigbee2mqtt/bridge/state")
		assert.Equal(t, 0, code)
		assert.Equal(t, "probe", received)
		assert.Equal(t, started, tc.brokerPodIdentity(t, ns, pod), "the broker pod restarted")
	})

	t.Run("a client is refused on a topic outside its ACL", func(t *testing.T) {
		_, code := tc.publish(ns, pod, "observer", "observer-pw", "homeassistant/secret", "observer-only")
		require.Equal(t, 0, code)
		out, _ := tc.receive(ns, pod, "zigbee2mqtt", "z2m-pw", "homeassistant/secret")
		assert.NotContains(t, out, "observer-only", "zigbee2mqtt read a topic outside its ACL")

		_, code = tc.publish(ns, pod, "zigbee2mqtt", "z2m-pw", "homeassistant/injected", "from-z2m")
		assert.Equal(t, 0, code, "a denied publish is dropped silently (M21), not refused")
		out, _ = tc.receive(ns, pod, "observer", "observer-pw", "homeassistant/injected")
		assert.NotContains(t, out, "from-z2m", "zigbee2mqtt wrote a topic outside its ACL")
	})

	t.Run("a second user with another user's client ID does not take over its session", func(t *testing.T) {
		done, out, stop := tc.startSubscriber(t, ns, pod, "zigbee2mqtt", "z2m-pw", "zigbee2mqtt/takeover",
			"-i", "z2m-session", "-C", "1", "-W", "60")
		defer stop()
		time.Sleep(3 * time.Second)

		_, code := tc.mqtt(ns, pod, "mosquitto_pub", "-h", "127.0.0.1", "-p", "1883", "-u", "observer", "-P", "observer-pw",
			"-i", "z2m-session", "-t", "zigbee2mqtt/takeover", "-m", "after-the-attempt")
		require.Equal(t, 0, code)

		select {
		case <-done:
		case <-time.After(60 * time.Second):
			t.Fatalf("the subscriber never received the message: %s", out.String())
		}
		assert.Contains(t, out.String(), "after-the-attempt")

		logs, err := tc.kube.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{Container: "mosquitto"}).
			DoRaw(context.Background())
		require.NoError(t, err)
		assert.NotContains(t, string(logs), "session taken over",
			"use_username_as_clientid makes the client ID the username, so another user cannot reach the session (M17)")
	})

	t.Run("a changed password reaches the broker with no edit to any custom resource", func(t *testing.T) {
		tc.setPassword(t, ns, "z2m-mqtt", "z2m-rotated")

		tc.eventuallyRefused(t, ns, pod, "zigbee2mqtt", "z2m-pw")
		tc.eventuallyAccepted(t, ns, pod, "zigbee2mqtt", "z2m-rotated", "zigbee2mqtt/rotated")
		assert.Equal(t, started, tc.brokerPodIdentity(t, ns, pod), "the broker pod restarted")
	})

	t.Run("a deleted user's live connection is dropped", func(t *testing.T) {
		done, out, stop := tc.startSubscriber(t, ns, pod, "zigbee2mqtt", "z2m-rotated", "zigbee2mqtt/#")
		defer stop()
		time.Sleep(3 * time.Second)
		select {
		case <-done:
			t.Fatalf("the subscriber did not stay connected: %s", out.String())
		default:
		}

		err := tc.dynamic.Resource(mosquittoUserGVR).Namespace(ns).Delete(context.Background(), "zigbee2mqtt", metav1.DeleteOptions{})
		require.NoError(t, err)

		select {
		case <-done:
		case <-time.After(reloadTimeout):
			t.Fatalf("the deleted user's connection was still open after %s", reloadTimeout)
		}
		tc.eventuallyRefused(t, ns, pod, "zigbee2mqtt", "z2m-rotated")
		assert.Equal(t, started, tc.brokerPodIdentity(t, ns, pod), "the broker pod restarted")
	})

	t.Run("a spec.config with a second listener is refused and the broker keeps serving", func(t *testing.T) {
		tc.updateMosquittoSpec(t, ns, name, map[string]interface{}{
			"config": "max_keepalive 120\nlistener 1884\nlistener_allow_anonymous true\n",
		})
		err := wait.PollUntilContextTimeout(context.Background(), pollInterval, testTimeout, true,
			func(ctx context.Context) (bool, error) {
				status := tc.getMosquittoStatus(t, ns, name)
				conditions, _ := status["conditions"].([]interface{})
				for _, raw := range conditions {
					cond, _ := raw.(map[string]interface{})
					if cond["type"] == "Ready" && cond["reason"] == "ConfigDirectiveRefused" {
						return true, nil
					}
				}
				return false, nil
			})
		require.NoError(t, err, "the refused spec.config was never reported")

		conf := tc.getConfigMap(t, ns, name+"-config").Data["mosquitto.conf"]
		assert.NotContains(t, conf, "listener 1884", "the refused line reached the configuration")
		_, code := tc.publish(ns, pod, "observer", "observer-pw", "e2e/still-serving", "yes")
		assert.Equal(t, 0, code)
		assert.Equal(t, started, tc.brokerPodIdentity(t, ns, pod), "the broker pod restarted")
	})

	t.Run("the rendered Secret holds hashes only", func(t *testing.T) {
		secret, err := tc.kube.CoreV1().Secrets(ns).Get(context.Background(), name+"-auth", metav1.GetOptions{})
		require.NoError(t, err)
		passwd := string(secret.Data["passwd"])
		assert.True(t, strings.HasPrefix(passwd, "observer:$7$1000$"), "unexpected passwd shape")
		assert.NotContains(t, passwd, "observer-pw")
	})

	t.Run("deleting the CR collects the rendered Secret too", func(t *testing.T) {
		tc.deleteMosquitto(t, ns, name)
		tc.waitForMosquittoDeleted(t, ns, name)
		err := wait.PollUntilContextTimeout(context.Background(), pollInterval, garbageCollectionTimeout, true,
			func(ctx context.Context) (bool, error) {
				_, err := tc.kube.CoreV1().Secrets(ns).Get(ctx, name+"-auth", metav1.GetOptions{})
				return apierrors.IsNotFound(err), nil
			})
		require.NoError(t, err, "%s-auth was not collected with its Mosquitto", name)
	})
}
