package exporter

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// brokerCert is a self-signed certificate that names the broker's DNS name and
// not 127.0.0.1, the shape a broker certificate has: the exporter connects to
// 127.0.0.1, so a host name check would refuse every one of them.
func brokerCert(t *testing.T, serial int64) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "broker"},
		DNSNames:     []string{"broker.example"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// serveTLS starts a TLS server on 127.0.0.1 that presents cert.
func serveTLS(t *testing.T, cert tls.Certificate) string {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the refused handshakes are the point
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

func handshake(addr string, cfg *tls.Config) error {
	conn, err := tls.Dial("tcp", addr, cfg)
	if err != nil {
		return err
	}
	return conn.Close()
}

// TestPinnedTLS_AcceptsExactlyTheMountedCertificate is ADR 0002 D3 as amended
// (open decision 7c): under TLS the exporter accepts the certificate mounted at
// --tls-cert and nothing else, checks no chain and no host name, and reads the
// file on every handshake, so a renewed certificate is accepted with the same
// configuration once the broker serves it.
func TestPinnedTLS_AcceptsExactlyTheMountedCertificate(t *testing.T) {
	certA, pemA := brokerCert(t, 1)
	certB, pemB := brokerCert(t, 2)
	mounted := filepath.Join(t.TempDir(), "tls.crt")
	require.NoError(t, os.WriteFile(mounted, pemA, 0o600))

	cfg := pinnedTLS(mounted)
	serverA, serverB := serveTLS(t, certA), serveTLS(t, certB)

	require.NoError(t, handshake(serverA, cfg),
		"the mounted certificate must be accepted on 127.0.0.1, although it names only broker.example")
	require.ErrorContains(t, handshake(serverB, cfg), "the broker serves a certificate other than the mounted one",
		"a broker serving any other certificate must be refused")

	require.NoError(t, os.WriteFile(mounted, pemB, 0o600))
	require.NoError(t, handshake(serverB, cfg),
		"a renewed certificate must be accepted by the same configuration: the mount is read on every handshake")
	require.Error(t, handshake(serverA, cfg), "the replaced certificate must be refused once the mount holds the new one")

	require.NoError(t, os.Remove(mounted))
	require.Error(t, handshake(serverB, cfg), "without the mounted certificate nothing is accepted")
}
