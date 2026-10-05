package reloader

import (
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"
)

// The keys of a TLS Secret the broker serves (ADR 0001).
const (
	tlsCertFile = "tls.crt"
	tlsKeyFile  = "tls.key"
)

// tlsState is what the reloader last saw of the mounted TLS Secret.
type tlsState struct {
	crt, key [sha256.Size]byte
	seen     bool
	valid    bool
}

// readTLS reads tls.crt and tls.key through one resolved ..data link, so the
// two come from one published version of the Secret (M23).
func readTLS(dir string) ([]byte, []byte, error) {
	base := resolveData(dir)
	crt, err := os.ReadFile(filepath.Join(base, tlsCertFile)) // #nosec G304 -- a fixed key under the pod's own TLS mount
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", tlsCertFile, err)
	}
	key, err := os.ReadFile(filepath.Join(base, tlsKeyFile)) // #nosec G304 -- a fixed key under the pod's own TLS mount
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", tlsKeyFile, err)
	}
	return crt, key, nil
}

// checkTLS compares the mounted TLS pair with what was seen before and checks
// that the certificate and the key belong together (ADR 0001 D10, ADR 0014 D5).
// It reports whether the pair changed since the last call, and an error only
// when it did and the new pair is not usable - so a broken pair is reported
// once, not every round. The first call records the pair the broker started
// with and reports no change. state.valid says whether the pair now mounted
// may be signalled.
func checkTLS(dir string, state *tlsState) (changed bool, err error) {
	crt, key, readErr := readTLS(dir)
	crtSum, keySum := sha256.Sum256(crt), sha256.Sum256(key)
	if state.seen && crtSum == state.crt && keySum == state.key {
		return false, nil
	}
	first := !state.seen
	state.seen, state.crt, state.key = true, crtSum, keySum
	if readErr == nil {
		if _, pairErr := tls.X509KeyPair(crt, key); pairErr != nil {
			readErr = fmt.Errorf("tls.crt and tls.key do not form a valid pair: %w", pairErr)
		}
	}
	state.valid = readErr == nil
	return !first, readErr
}
