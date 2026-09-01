package testimages

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The pin is read by the E2E suite, which cannot assert on itself, so what the
// selector does with each input is pinned here instead.

func TestDefault_UsesThePinWhenNothingIsSelected(t *testing.T) {
	assert.Equal(t, MosquittoImage, Default(),
		"the suites provision the pinned broker image unless told otherwise")
}

// TestDefault_ExplicitImageWins covers the developer knob: trying an image that
// is not the pin must not be overridden by the pin.
func TestDefault_ExplicitImageWins(t *testing.T) {
	t.Setenv(EnvMosquittoImage, "eclipse-mosquitto:2.0.21")

	assert.Equal(t, "eclipse-mosquitto:2.0.21", Default())
}

// TestDefault_EmptyEnvFallsBackToThePin is the case the Makefile produces: it
// passes E2E_MOSQUITTO_IMAGE through unconditionally, so the variable is present
// and empty on every run that does not override it.
func TestDefault_EmptyEnvFallsBackToThePin(t *testing.T) {
	t.Setenv(EnvMosquittoImage, "")

	assert.Equal(t, MosquittoImage, Default())
}

// TestMosquittoImageIsPinnedToATag guards the property Renovate's regex depends
// on: a digest-only or tagless reference would stop the custom manager matching,
// and the pin would silently stop being updated.
func TestMosquittoImageIsPinnedToATag(t *testing.T) {
	repository, tag, found := strings.Cut(MosquittoImage, ":")
	require.True(t, found, "the pin must carry an explicit tag")
	assert.Equal(t, "eclipse-mosquitto", repository)
	assert.True(t, strings.HasPrefix(tag, "2."),
		"the pin stays on the 2.x line; see the allowedVersions rule in renovate.json")
}
