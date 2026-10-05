package auth

import (
	"bytes"
	"crypto/rand"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// referenceHash is a line mosquitto_passwd of eclipse-mosquitto:2.1.2-alpine
// wrote for the password "s3cret" (broker-behaviour.md M20). It pins the
// verifier against the broker's own output rather than against this package's.
const referenceHash = "$7$1000$MvLfuap+p7TiHi/VmwutNaVddP9oa1VKGGj71wfZ5pvL2d1Bmo8dKj7KZa6MjAEOE/8YJdvPPrG+Cm0vNu4RHg==" +
	"$dqyaHMERHy43zDnUuKY3zFA1rk1JfssV8YuH5JB9e3F4pajPH0P3z+y6LdyndzhpacWRnPy1fmbLYbhC/68qeA=="

// mosquittoPasswdShape is the shape of every $7$ hash mosquitto_passwd writes:
// 1000 iterations, 88 characters of padded base64 for the 64-byte salt and the
// 64-byte key.
var mosquittoPasswdShape = regexp.MustCompile(`^\$7\$1000\$[A-Za-z0-9+/]{86}==\$[A-Za-z0-9+/]{86}==$`)

func TestVerifyPassword_AcceptsTheBrokersOwnHash(t *testing.T) {
	assert.True(t, VerifyPassword(referenceHash, "s3cret"))
	assert.False(t, VerifyPassword(referenceHash, "s3cret "))
	assert.False(t, VerifyPassword(referenceHash, ""))
}

func TestHashPassword_HasTheShapeMosquittoPasswdWrites(t *testing.T) {
	require.Regexp(t, mosquittoPasswdShape, referenceHash, "the reference itself must have the shape")

	encoded, err := HashPassword("s3cret", rand.Reader)
	require.NoError(t, err)

	assert.Regexp(t, mosquittoPasswdShape, encoded)
	assert.True(t, VerifyPassword(encoded, "s3cret"))
	assert.False(t, VerifyPassword(encoded, "other"))
}

func TestHashPassword_SaltComesFromTheReader(t *testing.T) {
	zeros := bytes.NewReader(make([]byte, 128))
	first, err := HashPassword("pw", zeros)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(first, "$7$1000$"+strings.Repeat("A", 86)+"==$"),
		"a 64-byte zero salt is 86 A and padding: the salt is the reader's bytes, not derived from anything")

	_, err = HashPassword("pw", bytes.NewReader(nil))
	assert.Error(t, err, "an exhausted reader must fail, not hash with a short or empty salt")
}

func TestVerifyPassword_RefusesWhatItCannotParse(t *testing.T) {
	for _, encoded := range []string{
		"",
		"s3cret",
		"$6$salt$hash",
		"$7$1000$onlysalt",
		"$7$x$c2FsdA==$a2V5",
		"$7$0$c2FsdA==$a2V5",
		"$7$1000$!!!$a2V5",
		"$7$1000$c2FsdA==$!!!",
		"$7$1000$$a2V5",
	} {
		assert.False(t, VerifyPassword(encoded, "s3cret"), "%q", encoded)
	}
}
