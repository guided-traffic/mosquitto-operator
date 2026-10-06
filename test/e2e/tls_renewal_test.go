//go:build e2e

package e2e

// A renewed certificate reaches a running broker without a restart (ADR 0001
// D10, ADR 0014 D5). The reloader sidecar sees the kubelet publish the renewed
// Secret, checks that certificate and key form a pair, and sends the SIGHUP the
// broker reloads its TLS material on (M12). A broken pair is never signalled:
// the broker would drop every new handshake instead of falling back.
//
// The served certificate is read by a fresh TLS handshake from the test process
// through `kubectl port-forward`; the broker image has no openssl binary.

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/guided-traffic/mosquitto-operator/test/testimages"
)

// TestE2E_TLS_ACertManagerRenewalIsReloaded: cert-manager re-issues the
// Certificate, a fresh handshake sees the new serial, the pod was not
// restarted, and a subscriber connected before the renewal still receives.
func TestE2E_TLS_ACertManagerRenewalIsReloaded(t *testing.T) {
	t.Parallel()
	tc := newTestClients(t)

	ns := "e2e-tls-renewal"
	cleanup := tc.createNamespace(t, ns)
	defer cleanup()

	name := "renewed"
	secretName := name + "-tls"
	pod := name + "-0"
	podDNS := fmt.Sprintf("%s-0.%s-headless.%s.svc.cluster.local", name, name, ns)

	tc.createCertificate(t, ns, secretName, []string{podDNS})
	tc.waitForCertificateReady(t, ns, secretName)
	tc.waitForSecret(t, ns, secretName)
	// Two users: the broker binds the client ID to the username, so a
	// publisher logged in as the subscriber's user would take its session over.
	tc.createCredentials(t, ns, "probe-mqtt", "probe", "probe-pw")
	tc.createUser(t, ns, "probe", name, "probe-mqtt", acl("e2e/#", "readwrite"))
	tc.createCredentials(t, ns, "watcher-mqtt", "watcher", "watcher-pw")
	tc.createUser(t, ns, "watcher", name, "watcher-mqtt", acl("e2e/#", "read"))

	tc.createMosquitto(t, ns, buildMosquittoObject(name, ns, map[string]interface{}{
		"replicas": int64(1),
		"image":    testimages.Default(),
		"tls":      map[string]interface{}{"secretName": secretName},
	}))
	defer tc.deleteMosquitto(t, ns, name)
	tc.waitForStatefulSetReady(t, ns, name, 1)
	tc.waitForUserReady(t, ns, "probe")
	tc.waitForUserReady(t, ns, "watcher")

	secret := tc.getSecret(t, ns, secretName)
	roots := certPool(t, secret.Data["ca.crt"])
	issued := secretSerial(t, secret)
	served := tc.eventuallyServed(t, ns, pod, podDNS, roots, func(serial string) bool { return serial == issued })
	t.Logf("serving serial %s", served)

	tlsServer := []string{"-h", podDNS, "-p", "8883", "--cafile", caCertPath, "-q", "1"}
	tlsClient := append([]string{"-u", "probe", "-P", "probe-pw"}, tlsServer...)
	subscriber := append([]string{"mosquitto_sub", "-u", "watcher", "-P", "watcher-pw", "-t", "e2e/renewal"}, tlsServer...)
	done, out, stop := tc.startClient(t, ns, pod, subscriber...)
	defer stop()
	tc.eventuallyDelivered(t, ns, pod, tlsClient, out, "before-the-renewal")
	before := tc.brokerPodIdentity(t, ns, pod)

	t.Log("Triggering a renewal, as `cmctl renew` does")
	tc.renewCertificate(t, ns, secretName)
	renewed := tc.waitForSecretSerialChange(t, ns, secretName, issued)

	served = tc.eventuallyServed(t, ns, pod, podDNS, roots, func(serial string) bool { return serial == renewed })
	assert.Equal(t, renewed, served, "a fresh handshake sees the renewed certificate")
	assert.Equal(t, before, tc.brokerPodIdentity(t, ns, pod), "the renewal was reloaded, not restarted")

	select {
	case <-done:
		t.Fatalf("the subscriber connected before the renewal was disconnected: %s", out.String())
	default:
	}
	tc.eventuallyDelivered(t, ns, pod, tlsClient, out, "after-the-renewal")
}

// TestE2E_TLS_AMismatchedPairIsNeverLoaded: a TLS Secret edited by hand to a
// certificate with the key of another pair. The reloader reports it and sends
// no signal, so the broker keeps serving the previous certificate to new
// handshakes; the next valid pair is loaded.
func TestE2E_TLS_AMismatchedPairIsNeverLoaded(t *testing.T) {
	t.Parallel()
	tc := newTestClients(t)

	ns := "e2e-tls-mismatch"
	cleanup := tc.createNamespace(t, ns)
	defer cleanup()

	name := "mismatch"
	secretName := name + "-tls"
	pod := name + "-0"
	podDNS := fmt.Sprintf("%s-0.%s-headless.%s.svc.cluster.local", name, name, ns)

	ca := newTestCA(t)
	first, second, third := ca.issue(t, podDNS, 101), ca.issue(t, podDNS, 102), ca.issue(t, podDNS, 103)
	_, err := tc.kube.CoreV1().Secrets(ns).Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"tls.crt": first.crt, "tls.key": first.key},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	tc.createCredentials(t, ns, "probe-mqtt", "probe", "probe-pw")
	tc.createUser(t, ns, "probe", name, "probe-mqtt", acl("e2e/#", "readwrite"))

	tc.createMosquitto(t, ns, buildMosquittoObject(name, ns, map[string]interface{}{
		"replicas": int64(1),
		"image":    testimages.Default(),
		"tls":      map[string]interface{}{"secretName": secretName},
	}))
	defer tc.deleteMosquitto(t, ns, name)
	tc.waitForStatefulSetReady(t, ns, name, 1)
	tc.waitForUserReady(t, ns, "probe")

	roots := ca.pool()
	tc.eventuallyServed(t, ns, pod, podDNS, roots, func(serial string) bool { return serial == "101" })
	before := tc.brokerPodIdentity(t, ns, pod)

	t.Log("Editing the Secret to a certificate with the key of another pair")
	tc.setTLSPair(t, ns, secretName, second.crt, first.key)
	tc.waitForReloaderLog(t, ns, pod, "the TLS pair is not valid")

	served, err := tc.servedSerial(ns, pod, podDNS, roots)
	require.NoError(t, err, "a new handshake still succeeds: the broker was not signalled")
	assert.Equal(t, "101", served, "the previous certificate is still served")
	assert.Equal(t, before, tc.brokerPodIdentity(t, ns, pod))

	t.Log("Mounting a valid pair again")
	tc.setTLSPair(t, ns, secretName, third.crt, third.key)
	served = tc.eventuallyServed(t, ns, pod, podDNS, roots, func(serial string) bool { return serial == "103" })
	assert.Equal(t, "103", served)
	assert.Equal(t, before, tc.brokerPodIdentity(t, ns, pod))
}

// --- helpers ---

// eventuallyDelivered publishes over TLS until the running subscriber shows
// the payload. A publish is retried because a subscription made a moment
// earlier may not be in place yet.
func (tc *testClients) eventuallyDelivered(t *testing.T, ns, pod string, client []string, out *syncBuffer, payload string) {
	t.Helper()
	err := wait.PollUntilContextTimeout(context.Background(), 2*time.Second, testTimeout, true,
		func(context.Context) (bool, error) {
			if strings.Contains(out.String(), payload) {
				return true, nil
			}
			pub := append(append([]string{"mosquitto_pub"}, client...), "-t", "e2e/renewal", "-m", payload)
			if msg, code := tc.mqtt(ns, pod, pub...); code != 0 {
				t.Logf("publish %q: rc=%d %s", payload, code, msg)
			}
			return false, nil
		})
	require.NoError(t, err, "the subscriber never received %q: %s", payload, out.String())
}

// renewCertificate sets the Issuing condition on a Certificate, which is what
// `cmctl renew` does to make cert-manager re-issue it.
func (tc *testClients) renewCertificate(t *testing.T, ns, name string) {
	t.Helper()
	ctx := context.Background()
	cert, err := tc.dynamic.Resource(certificateGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	conditions, _, _ := unstructured.NestedSlice(cert.Object, "status", "conditions")
	conditions = append(conditions, map[string]interface{}{
		"type":               "Issuing",
		"status":             "True",
		"reason":             "ManuallyTriggered",
		"message":            "Certificate re-issuance manually triggered",
		"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
	})
	require.NoError(t, unstructured.SetNestedSlice(cert.Object, conditions, "status", "conditions"))
	_, err = tc.dynamic.Resource(certificateGVR).Namespace(ns).UpdateStatus(ctx, cert, metav1.UpdateOptions{})
	require.NoError(t, err, "triggering the renewal of Certificate %s/%s", ns, name)
}

// waitForSecretSerialChange waits until the TLS Secret carries a certificate
// with another serial, and returns it.
func (tc *testClients) waitForSecretSerialChange(t *testing.T, ns, name, previous string) string {
	t.Helper()
	var serial string
	err := wait.PollUntilContextTimeout(context.Background(), pollInterval, testTimeout, true,
		func(ctx context.Context) (bool, error) {
			secret, err := tc.kube.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, nil
			}
			serial = secretSerial(t, secret)
			return serial != previous, nil
		})
	require.NoError(t, err, "cert-manager did not re-issue %s/%s", ns, name)
	return serial
}

// setTLSPair replaces the certificate and key of a TLS Secret in one update.
func (tc *testClients) setTLSPair(t *testing.T, ns, name string, crt, key []byte) {
	t.Helper()
	ctx := context.Background()
	secret, err := tc.kube.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	secret.Data["tls.crt"], secret.Data["tls.key"] = crt, key
	_, err = tc.kube.CoreV1().Secrets(ns).Update(ctx, secret, metav1.UpdateOptions{})
	require.NoError(t, err)
}

// waitForReloaderLog waits until the reloader sidecar logged a line containing
// text: the observable proof that the kubelet published the edit to the pod.
func (tc *testClients) waitForReloaderLog(t *testing.T, ns, pod, text string) {
	t.Helper()
	err := wait.PollUntilContextTimeout(context.Background(), 5*time.Second, reloadTimeout, true,
		func(ctx context.Context) (bool, error) {
			raw, err := tc.kube.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{Container: "reloader"}).DoRaw(ctx)
			if err != nil {
				return false, nil
			}
			return strings.Contains(string(raw), text), nil
		})
	require.NoError(t, err, "the reloader in %s/%s never logged %q", ns, pod, text)
}

// eventuallyServed polls fresh handshakes until the served serial satisfies
// want, and returns it.
func (tc *testClients) eventuallyServed(t *testing.T, ns, pod, serverName string, roots *x509.CertPool, want func(string) bool) string {
	t.Helper()
	var serial string
	err := wait.PollUntilContextTimeout(context.Background(), 5*time.Second, reloadTimeout, true,
		func(context.Context) (bool, error) {
			s, err := tc.servedSerial(ns, pod, serverName, roots)
			if err != nil {
				t.Logf("handshake with %s/%s: %v", ns, pod, err)
				return false, nil
			}
			serial = s
			return want(s), nil
		})
	require.NoError(t, err, "the broker in %s/%s still serves serial %s", ns, pod, serial)
	return serial
}

var forwardingLine = regexp.MustCompile(`Forwarding from 127\.0\.0\.1:(\d+)`)

// portForward starts `kubectl port-forward` to a port of a pod and returns the
// local address. It ends with ctx.
func portForward(ctx context.Context, ns, pod string, port int) (string, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "port-forward", "-n", ns, "pod/"+pod, fmt.Sprintf(":%d", port))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	go func() { <-ctx.Done(); _ = cmd.Wait() }()

	local := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if m := forwardingLine.FindStringSubmatch(scanner.Text()); m != nil {
				select {
				case local <- m[1]:
				default:
				}
			}
		}
	}()
	select {
	case p := <-local:
		return "127.0.0.1:" + p, nil
	case <-ctx.Done():
		return "", fmt.Errorf("kubectl port-forward to %s/%s:%d did not start", ns, pod, port)
	}
}

// servedSerial opens a port-forward to the broker pod's MQTTS port, completes
// one verified TLS handshake through it and returns the serial of the served
// certificate.
func (tc *testClients) servedSerial(ns, pod, serverName string, roots *x509.CertPool) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	address, err := portForward(ctx, ns, pod, 8883)
	if err != nil {
		return "", err
	}
	dialer := &tls.Dialer{Config: &tls.Config{RootCAs: roots, ServerName: serverName, MinVersion: tls.VersionTLS12}}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()
	return conn.(*tls.Conn).ConnectionState().PeerCertificates[0].SerialNumber.String(), nil
}

// secretSerial is the serial of the certificate in a TLS Secret.
func secretSerial(t *testing.T, secret *corev1.Secret) string {
	t.Helper()
	block, _ := pem.Decode(secret.Data["tls.crt"])
	require.NotNil(t, block, "tls.crt of %s holds no PEM block", secret.Name)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return cert.SerialNumber.String()
}

func certPool(t *testing.T, pemCerts []byte) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(pemCerts), "no CA certificate to verify the broker against")
	return pool
}

// testCA is a throwaway CA for a TLS Secret written by hand.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

// testPair is a certificate and its key, PEM encoded.
type testPair struct{ crt, key []byte }

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "e2e-mismatch-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &testCA{cert: cert, key: key}
}

func (ca *testCA) pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	return pool
}

// issue signs a server certificate for dnsName with the given serial, on a key
// of its own.
func (ca *testCA) issue(t *testing.T, dnsName string, serial int64) testPair {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: dnsName},
		DNSNames:     []string{dnsName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return testPair{
		crt: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		key: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	}
}
