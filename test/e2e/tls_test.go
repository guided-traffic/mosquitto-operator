//go:build e2e

package e2e

// TLS: the operator consumes an existing Secret and serves MQTTS from it.
//
// The Secret here is issued by cert-manager, which is the second of the two
// supported ways of filling spec.tls.secretName (the first is `kubectl create
// secret tls`). The operator neither creates nor renews the Certificate: the test
// owns that object exactly as an administrator would, and asserts that the
// operator added none of its own.
//
// cert-manager reaches this test only through the API server. There is no
// cert-manager Go dependency anywhere in this repository -- the Certificate is
// written through the dynamic client as an unstructured object, which is what
// keeps the operator's module graph free of it.

import (
	"context"
	"fmt"
	"sort"
	"testing"

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

// certificateGVR is the GroupVersionResource of the cert-manager Certificate CRD.
var certificateGVR = schema.GroupVersionResource{
	Group:    "cert-manager.io",
	Version:  "v1",
	Resource: "certificates",
}

// e2eIssuerName is the ClusterIssuer the E2E job installs
// (test/e2e/testdata/cert-manager-issuer.yaml). It is backed by a CA, so the
// issued Secret carries a ca.crt the MQTT client can verify the broker against.
const e2eIssuerName = "e2e-ca-issuer"

// The paths the operator mounts the referenced Secret at.
const (
	tlsMountPath = "/mosquitto/tls"
	caCertPath   = tlsMountPath + "/ca.crt"
)

// TestE2E_TLS_CertManagerIssuedSecretServesMQTTS is the whole cert-manager story
// in one test: an administrator issues a Certificate, points spec.tls.secretName
// at the Secret it produces, and the broker serves MQTTS from it.
func TestE2E_TLS_CertManagerIssuedSecretServesMQTTS(t *testing.T) {
	t.Parallel()
	tc := newTestClients(t)

	ns := "e2e-tls"
	cleanup := tc.createNamespace(t, ns)
	defer cleanup()

	name := "tls-broker"
	secretName := name + "-tls"

	// The names the client below actually connects to. They are listed
	// explicitly rather than as a wildcard: mosquitto_pub verifies the hostname,
	// and a SAN list that does not contain the name being dialled fails the
	// handshake with an error that reads like a broker defect.
	podDNS := fmt.Sprintf("%s-0.%s-headless.%s.svc.cluster.local", name, name, ns)
	serviceDNS := fmt.Sprintf("%s.%s.svc.cluster.local", name, ns)

	t.Logf("Requesting a certificate from ClusterIssuer %s for %s", e2eIssuerName, podDNS)
	tc.createCertificate(t, ns, secretName, []string{podDNS, serviceDNS})
	tc.waitForCertificateReady(t, ns, secretName)
	tc.waitForSecret(t, ns, secretName)

	// Asserted on the key set, never on secret.Data: a testify failure renders the
	// value it was given, and secret.Data holds the private key. This log is
	// tee'd to a file and echoed into the job output, so a single red run would
	// publish tls.key. Naming the keys that ARE present keeps the diagnostic.
	secret := tc.getSecret(t, ns, secretName)
	presentKeys := make([]string, 0, len(secret.Data))
	for key := range secret.Data {
		presentKeys = append(presentKeys, key)
	}
	sort.Strings(presentKeys)
	for _, key := range []string{"tls.crt", "tls.key", "ca.crt"} {
		require.Contains(t, presentKeys, key,
			"cert-manager wrote no %s into %s; the secret carries %v", key, secretName, presentKeys)
	}

	t.Log("Creating a Mosquitto CR that references the issued secret")
	tc.createMosquitto(t, ns, buildMosquittoObject(name, ns, map[string]interface{}{
		"replicas": int64(1),
		"image":    testimages.Default(),
		"tls": map[string]interface{}{
			"secretName": secretName,
		},
	}))
	defer tc.deleteMosquitto(t, ns, name)

	tc.waitForStatefulSetReady(t, ns, name, 1)
	tc.waitForMosquittoPhase(t, ns, name, "Ready")

	t.Run("the secret is mounted and the listener moved to MQTTS", func(t *testing.T) {
		sts := tc.getStatefulSet(t, ns, name)

		var mounted bool
		for _, volume := range sts.Spec.Template.Spec.Volumes {
			if volume.Secret != nil && volume.Secret.SecretName == secretName {
				mounted = true
			}
		}
		assert.True(t, mounted, "the referenced secret is not a volume of the broker pod")

		require.Len(t, sts.Spec.Template.Spec.Containers, 1)
		container := sts.Spec.Template.Spec.Containers[0]

		var mountPath string
		for _, mount := range container.VolumeMounts {
			if mount.Name == "tls" {
				mountPath = mount.MountPath
				assert.True(t, mount.ReadOnly, "the TLS material must be mounted read-only")
			}
		}
		assert.Equal(t, tlsMountPath, mountPath)

		require.Len(t, container.Ports, 1, "TLS replaces the plain listener, it does not add to it")
		assert.Equal(t, "mqtts", container.Ports[0].Name)
		assert.Equal(t, int32(8883), container.Ports[0].ContainerPort)
	})

	t.Run("the generated configuration points at the mounted files", func(t *testing.T) {
		conf := tc.getConfigMap(t, ns, name+"-config").Data["mosquitto.conf"]
		require.NotEmpty(t, conf)
		assert.Contains(t, conf, "listener 8883")
		assert.Contains(t, conf, "certfile "+tlsMountPath+"/tls.crt")
		assert.Contains(t, conf, "keyfile "+tlsMountPath+"/tls.key")
		assert.NotContains(t, conf, "listener 1883", "the plaintext listener must be gone")
	})

	t.Run("both Services expose the MQTTS port", func(t *testing.T) {
		for _, svcName := range []string{name, name + "-headless"} {
			svc := tc.getService(t, ns, svcName)
			require.Len(t, svc.Spec.Ports, 1, "Service %s", svcName)
			assert.Equal(t, "mqtts", svc.Spec.Ports[0].Name, "Service %s", svcName)
			assert.Equal(t, int32(8883), svc.Spec.Ports[0].Port, "Service %s", svcName)
		}
	})

	t.Run("the broker completes a TLS MQTT session", func(t *testing.T) {
		pod := name + "-0"
		payload := "encrypted"

		// The CA of the issuer travels in the same secret, so the client verifies
		// the broker with material it did not have to be given separately. -h is
		// the pod's own DNS name and not localhost: a certificate is only proven
		// by a hostname the client verifies.
		tc.podExec(t, ns, pod,
			"mosquitto_pub", "-h", podDNS, "-p", "8883", "--cafile", caCertPath,
			"-q", "1", "-r", "-t", probeTopic, "-m", payload)

		received := tc.podExec(t, ns, pod,
			"mosquitto_sub", "-h", podDNS, "-p", "8883", "--cafile", caCertPath,
			"-q", "1", "-t", probeTopic, "-C", "1", "-W", "15")

		assert.Equal(t, payload, received,
			"the MQTTS listener did not complete a publish/subscribe round trip")
	})

	t.Run("the operator creates no Certificate of its own", func(t *testing.T) {
		// spec.tls consumes a Secret and nothing else. If the operator ever grew
		// a Certificate it would also own renewal, which the API deliberately
		// leaves with the administrator.
		certs, err := tc.dynamic.Resource(certificateGVR).Namespace(ns).List(
			context.Background(), metav1.ListOptions{})
		require.NoError(t, err, "Failed to list Certificates in %s", ns)

		names := make([]string, 0, len(certs.Items))
		for i := range certs.Items {
			names = append(names, certs.Items[i].GetName())
		}
		assert.Equal(t, []string{secretName}, names,
			"only the Certificate this test created may exist in %s", ns)
	})
}

// --- helpers ---

// createCertificate writes a cert-manager Certificate through the dynamic client.
// It is the object an administrator owns; nothing in this repository generates it.
func (tc *testClients) createCertificate(t *testing.T, namespace, name string, dnsNames []string) {
	t.Helper()

	names := make([]interface{}, 0, len(dnsNames))
	for _, dns := range dnsNames {
		names = append(names, dns)
	}

	cert := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "cert-manager.io/v1",
			"kind":       "Certificate",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				// The Secret cert-manager writes carries tls.crt, tls.key and,
				// because the issuer is a CA, ca.crt -- exactly the layout
				// spec.tls.secretName expects.
				"secretName": name,
				"dnsNames":   names,
				"issuerRef": map[string]interface{}{
					"name":  e2eIssuerName,
					"kind":  "ClusterIssuer",
					"group": "cert-manager.io",
				},
			},
		},
	}

	_, err := tc.dynamic.Resource(certificateGVR).Namespace(namespace).Create(
		context.Background(), cert, metav1.CreateOptions{})
	require.NoError(t, err, "Failed to create Certificate %s/%s", namespace, name)
}

// waitForCertificateReady waits until a cert-manager Certificate reports
// Ready=True.
func (tc *testClients) waitForCertificateReady(t *testing.T, namespace, name string) {
	t.Helper()

	err := wait.PollUntilContextTimeout(context.Background(), pollInterval, testTimeout, true,
		func(ctx context.Context) (bool, error) {
			cert, err := tc.dynamic.Resource(certificateGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) {
					return false, nil
				}
				return false, err
			}

			conditions, found, err := unstructured.NestedSlice(cert.Object, "status", "conditions")
			if err != nil || !found {
				return false, nil
			}
			for _, raw := range conditions {
				cond, ok := raw.(map[string]interface{})
				if !ok {
					continue
				}
				condType, _, _ := unstructured.NestedString(cond, "type")
				condStatus, _, _ := unstructured.NestedString(cond, "status")
				if condType == "Ready" && condStatus == "True" {
					return true, nil
				}
			}
			t.Logf("Waiting for Certificate %s/%s to become ready...", namespace, name)
			return false, nil
		})
	require.NoError(t, err, "Certificate %s/%s did not become ready", namespace, name)
}

// waitForSecret waits until a Secret exists.
func (tc *testClients) waitForSecret(t *testing.T, namespace, name string) {
	t.Helper()

	err := wait.PollUntilContextTimeout(context.Background(), pollInterval, testTimeout, true,
		func(ctx context.Context) (bool, error) {
			_, err := tc.kube.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) {
					return false, nil
				}
				return false, err
			}
			return true, nil
		})
	require.NoError(t, err,
		"Secret %s/%s was not created; cert-manager did not issue the certificate", namespace, name)
}

// getSecret retrieves a Secret.
func (tc *testClients) getSecret(t *testing.T, namespace, name string) *corev1.Secret {
	t.Helper()

	secret, err := tc.kube.CoreV1().Secrets(namespace).Get(
		context.Background(), name, metav1.GetOptions{})
	require.NoError(t, err, "Failed to get Secret %s/%s", namespace, name)
	return secret
}
