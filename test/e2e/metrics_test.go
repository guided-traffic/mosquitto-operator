//go:build e2e

package e2e

// The broker metrics exporter (ADR 0002): a container of the broker pod that
// logs in to its own broker over localhost as the reserved user mko-exporter,
// reads $SYS and serves it as /metrics on port 9234. Only a cluster shows the
// login against the rendered credentials, the pinned certificate under TLS and
// the scrape through the pod's port.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/guided-traffic/mosquitto-operator/test/testimages"
)

// TestE2E_Metrics_TheExporterServesTheBrokersSysTree runs one broker with
// metrics on a plain listener and one under TLS, scrapes both, and shows that a
// MosquittoUser cannot take the exporter's name.
func TestE2E_Metrics_TheExporterServesTheBrokersSysTree(t *testing.T) {
	t.Parallel()
	tc := newTestClients(t)

	ns := "e2e-metrics"
	cleanup := tc.createNamespace(t, ns)
	defer cleanup()

	plain, secure := "metered", "metered-tls"
	secretName := secure + "-tls"
	tc.createCertificate(t, ns, secretName, []string{fmt.Sprintf("%s-0.%s-headless.%s.svc.cluster.local", secure, secure, ns)})
	tc.waitForCertificateReady(t, ns, secretName)

	tc.createCredentials(t, ns, "probe-mqtt", "probe", "probe-pw")
	tc.createUser(t, ns, "probe", plain, "probe-mqtt", acl("e2e/#", "readwrite"))

	tc.createMosquitto(t, ns, buildMosquittoObject(plain, ns, map[string]interface{}{
		"replicas": int64(1),
		"image":    testimages.Default(),
		"metrics":  map[string]interface{}{"enabled": true},
	}))
	defer tc.deleteMosquitto(t, ns, plain)
	tc.createMosquitto(t, ns, buildMosquittoObject(secure, ns, map[string]interface{}{
		"replicas": int64(1),
		"image":    testimages.Default(),
		"metrics":  map[string]interface{}{"enabled": true},
		"tls":      map[string]interface{}{"secretName": secretName},
	}))
	defer tc.deleteMosquitto(t, ns, secure)
	tc.waitForStatefulSetReady(t, ns, plain, 1)
	tc.waitForStatefulSetReady(t, ns, secure, 1)
	tc.waitForUserReady(t, ns, "probe")

	t.Run("the exporter logs in and serves the broker's series", func(t *testing.T) {
		body := tc.eventuallyScraped(t, ns, plain+"-0", "mosquitto_exporter_connected 1")
		assert.Regexp(t, regexp.MustCompile(`mosquitto_version_info\{version="2\.1\.\d+"\} 1`), body)
		assert.Regexp(t, regexp.MustCompile(`(?m)^mosquitto_clients_connected [1-9]`), body, "the exporter is a client itself")
		assert.Contains(t, body, "# TYPE mosquitto_bytes_received_total counter")
		assert.Contains(t, body, `mosquitto_load_messages_received{window="1min"}`)
	})

	t.Run("under TLS the exporter pins the mounted certificate", func(t *testing.T) {
		body := tc.eventuallyScraped(t, ns, secure+"-0", "mosquitto_exporter_connected 1")
		assert.Contains(t, body, "mosquitto_uptime_seconds")
	})

	t.Run("a MosquittoUser cannot take the exporter's name", func(t *testing.T) {
		tc.createCredentials(t, ns, "claim-mqtt", "mko-exporter", "claimed")
		tc.createUser(t, ns, "claim", plain, "claim-mqtt", acl("e2e/#", "read"))
		tc.waitForUserReason(t, ns, "claim", "UsernameReserved")
		_, code := tc.mqtt(ns, plain+"-0", "mosquitto_sub", "-h", "127.0.0.1", "-u", "mko-exporter", "-P", "claimed",
			"-t", "$SYS/broker/version", "-C", "1", "-W", "5")
		assert.Equal(t, 5, code, "the claimant's password must not log in as mko-exporter")
		tc.eventuallyScraped(t, ns, plain+"-0", "mosquitto_exporter_connected 1")
	})
}

// eventuallyScraped scrapes the exporter of a pod through a port-forward until
// one line of the body is want, and returns that body. A whole line, because a
// HELP text may contain a sample line.
func (tc *testClients) eventuallyScraped(t *testing.T, ns, pod, want string) string {
	t.Helper()
	var body string
	err := wait.PollUntilContextTimeout(context.Background(), 3*time.Second, testTimeout, true,
		func(context.Context) (bool, error) {
			b, err := scrapeExporter(ns, pod)
			if err != nil {
				t.Logf("scraping %s/%s: %v", ns, pod, err)
				return false, nil
			}
			body = b
			for _, line := range strings.Split(body, "\n") {
				if line == want {
					return true, nil
				}
			}
			return false, nil
		})
	require.NoError(t, err, "the exporter of %s/%s never served %q; last scrape:\n%s", ns, pod, want, body)
	return body
}

func scrapeExporter(ns, pod string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	address, err := portForward(ctx, ns, pod, 9234)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/metrics", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	return string(raw), err
}
