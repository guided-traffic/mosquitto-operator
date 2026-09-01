// Package testimages pins the Mosquitto broker image the test suites provision,
// in one place, so Renovate can keep it current.
//
// Only the tiers that actually pull an image use this constant. Unit and
// integration tests never do -- envtest starts an API server and etcd and no
// kubelet, so nothing there runs a container -- and their image strings are
// fixtures whose only requirement is to differ from one another. Pinning those
// would churn call sites on every Renovate bump without changing a byte that
// executes.
package testimages

import "os"

// EnvMosquittoImage names the broker image the E2E suite provisions, for trying
// an image that is not the pin at all. The Makefile passes it through to
// `make test-e2e`.
//
// The workflow carries no copy of the pin, deliberately: a second copy that lags
// turns a leg green while it tests the wrong image.
const EnvMosquittoImage = "E2E_MOSQUITTO_IMAGE"

const (
	// MosquittoImage is the broker image every suite provisions unless the
	// environment names another one. It is kept on the 2.x line by a packageRule
	// in renovate.json: the generated mosquitto.conf, the config and data paths
	// and the image tool check all assume the 2.x layout, so a future major is a
	// decision somebody makes here rather than a pull request that arrives on its
	// own.
	//
	// renovate: datasource=docker depName=eclipse-mosquitto
	MosquittoImage = "eclipse-mosquitto:2.1.2-alpine"
)

// Default is the image a test creates its broker with unless it has a reason to
// name a specific one.
func Default() string {
	if image := os.Getenv(EnvMosquittoImage); image != "" {
		return image
	}
	return MosquittoImage
}
