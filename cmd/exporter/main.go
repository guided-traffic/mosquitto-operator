// Command exporter serves the $SYS statistics of the broker in its own pod for
// Prometheus. It is the second binary of the operator image (ADR 0002 D1, D2)
// and runs as the exporter container of a broker pod with spec.metrics enabled.
package main

import (
	"os"

	"github.com/guided-traffic/mosquitto-operator/internal/exporter"
)

func main() {
	os.Exit(exporter.Main(os.Args[1:], os.Stderr))
}
