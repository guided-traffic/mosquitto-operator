package reloader

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pair is a self-signed certificate and its key, PEM encoded.
type pair struct{ crt, key string }

func newPair(t *testing.T, serial int64) pair {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "broker"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return pair{
		crt: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		key: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
	}
}

func publishTLS(t *testing.T, dir, version string, crt, key string) {
	t.Helper()
	publish(t, dir, version, map[string]string{tlsCertFile: crt, tlsKeyFile: key})
}

func TestCheckTLS(t *testing.T) {
	dir := t.TempDir()
	a, b := newPair(t, 1), newPair(t, 2)
	var state tlsState

	publishTLS(t, dir, "1", a.crt, a.key)
	changed, err := checkTLS(dir, &state)
	require.NoError(t, err)
	assert.False(t, changed, "the first pair is the one the broker started with")
	assert.True(t, state.valid)

	changed, err = checkTLS(dir, &state)
	require.NoError(t, err)
	assert.False(t, changed)

	publishTLS(t, dir, "2", b.crt, b.key)
	changed, err = checkTLS(dir, &state)
	require.NoError(t, err)
	assert.True(t, changed, "a renewed pair is a change")
	assert.True(t, state.valid)

	publishTLS(t, dir, "3", a.crt, b.key)
	changed, err = checkTLS(dir, &state)
	require.ErrorContains(t, err, "do not form a valid pair")
	assert.True(t, changed)
	assert.False(t, state.valid, "a certificate with the key of another pair is never signalled")

	changed, err = checkTLS(dir, &state)
	require.NoError(t, err, "an unchanged broken pair is reported once, not every round")
	assert.False(t, changed)
	assert.False(t, state.valid, "and it stays blocked")
}

// tlsRound is a round over a credentials mount and a TLS mount, with a broker
// process and a recorder for the signals.
func tlsRound(t *testing.T) (*round, *recorder, string) {
	t.Helper()
	cfg := testConfig(t)
	cfg.TLSDir = t.TempDir()
	cfg.ProcRoot = t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(cfg.ProcRoot, "7"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cfg.ProcRoot, "7", "comm"), []byte("mosquitto\n"), 0o644))
	rec := &recorder{}
	cfg.Signal = rec.send
	publish(t, cfg.Source, "1", map[string]string{"passwd": "a\n", "acl": "a\n"})
	_, err := Sync(cfg) // what auth-init did
	require.NoError(t, err)
	return &round{cfg: cfg, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, rec, cfg.TLSDir
}

func TestRound_ARenewedCertificateIsSignalled(t *testing.T) {
	r, rec, tlsDir := tlsRound(t)
	a, b := newPair(t, 1), newPair(t, 2)
	publishTLS(t, tlsDir, "1", a.crt, a.key)

	r.once()
	assert.Zero(t, rec.count(), "nothing changed since the broker started")

	publishTLS(t, tlsDir, "2", b.crt, b.key)
	r.once()
	assert.Equal(t, 1, rec.count(), "a renewed, valid pair is signalled")
	r.once()
	assert.Equal(t, 1, rec.count(), "once")
}

// TestRound_AnInvalidPairBlocksEverySignal: a SIGHUP reloads the certificate
// as well, and a broken pair takes the listener down (M12). So while the
// mounted pair is invalid nothing is signalled - not the pair, and not a
// credential change either. The credentials are still copied, and the pending
// signal goes out once a valid pair is mounted.
func TestRound_AnInvalidPairBlocksEverySignal(t *testing.T) {
	r, rec, tlsDir := tlsRound(t)
	a, b, c := newPair(t, 1), newPair(t, 2), newPair(t, 3)
	publishTLS(t, tlsDir, "1", a.crt, a.key)
	r.once()

	publishTLS(t, tlsDir, "2", b.crt, a.key)
	r.once()
	assert.Zero(t, rec.count(), "a mismatched pair is never signalled")

	publish(t, r.cfg.Source, "2", map[string]string{"passwd": "a\nb\n", "acl": "a\n"})
	r.once()
	r.once()
	assert.Zero(t, rec.count(), "a credential change waits while the mounted pair is invalid")
	assert.Equal(t, "a\nb\n", read(t, filepath.Join(r.cfg.Target, "passwd")), "the copy happened anyway")

	publishTLS(t, tlsDir, "3", c.crt, c.key)
	r.once()
	assert.Equal(t, 1, rec.count(), "a valid pair releases the pending reload, in one signal")
}

func TestRound_ABrokerStartedWithoutTLSIgnoresTheTLSPath(t *testing.T) {
	r, rec, _ := tlsRound(t)
	r.cfg.TLSDir = ""
	publish(t, r.cfg.Source, "2", map[string]string{"passwd": "a\nb\n", "acl": "a\n"})
	r.once()
	assert.Equal(t, 1, rec.count())
}
