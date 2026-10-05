// Package auth turns the users bound to a broker into what the broker's
// password-file and acl-file plugins read (ADR 0013, ADR 0014).
package auth

import (
	"crypto/pbkdf2"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	// HashIterations is the PBKDF2 iteration count of every hash the operator
	// writes. It is what mosquitto_passwd of the pinned image writes, and what
	// every 2.x build verifies (ADR 0014 D3, broker-behaviour.md M18): more
	// iterations protect only a hash that leaks without its plaintext, and cost
	// the broker the full hashing time on every failed login.
	HashIterations = 1000

	// saltLength and keyLength are the byte lengths mosquitto_passwd uses for
	// the $7$ format: 64 bytes each, 88 characters of padded base64 (M20).
	saltLength = 64
	keyLength  = 64

	// hashPrefix names the PBKDF2-SHA512 format of the password-file plugin.
	hashPrefix = "$7$"
)

// HashPassword returns the $7$ hash of password in the format the
// password-file plugin verifies: $7$<iterations>$<base64 salt>$<base64 key>,
// standard base64 with padding, the salt read from random.
func HashPassword(password string, random io.Reader) (string, error) {
	salt := make([]byte, saltLength)
	if _, err := io.ReadFull(random, salt); err != nil {
		return "", fmt.Errorf("reading a salt: %w", err)
	}
	key, err := pbkdf2.Key(sha512.New, password, salt, HashIterations, keyLength)
	if err != nil {
		return "", fmt.Errorf("deriving the key: %w", err)
	}
	return fmt.Sprintf("%s%d$%s$%s", hashPrefix, HashIterations,
		base64.StdEncoding.EncodeToString(salt), base64.StdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password verifies against a $7$ hash, with
// the iteration count and the salt the hash carries. A hash in any other
// format, or one that does not parse, never verifies.
func VerifyPassword(encoded, password string) bool {
	iterations, salt, key, err := parseHash(encoded)
	if err != nil {
		return false
	}
	derived, err := pbkdf2.Key(sha512.New, password, salt, iterations, len(key))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(derived, key) == 1
}

// errMalformedHash is what parseHash returns for anything that is not a $7$
// hash it can verify.
var errMalformedHash = errors.New("not a $7$ PBKDF2-SHA512 hash")

// parseHash splits a $7$ hash into its iteration count, salt and key.
func parseHash(encoded string) (int, []byte, []byte, error) {
	if !strings.HasPrefix(encoded, hashPrefix) {
		return 0, nil, nil, errMalformedHash
	}
	parts := strings.Split(strings.TrimPrefix(encoded, hashPrefix), "$")
	if len(parts) != 3 {
		return 0, nil, nil, errMalformedHash
	}
	iterations, err := strconv.Atoi(parts[0])
	if err != nil || iterations < 1 {
		return 0, nil, nil, errMalformedHash
	}
	salt, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil || len(salt) == 0 {
		return 0, nil, nil, errMalformedHash
	}
	key, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil || len(key) == 0 {
		return 0, nil, nil, errMalformedHash
	}
	return iterations, salt, key, nil
}
