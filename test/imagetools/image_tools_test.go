//go:build imagetools

// Package imagetools checks that the pinned Mosquitto image actually contains
// what this repository executes inside it.
//
// The operator does not build that image. It consumes the upstream one and runs
// one binary in it: the broker container's command names an absolute path
// (internal/builder/statefulset.go), so a rebase that moves or renames the
// executable turns every broker pod into a CrashLoopBackOff. The E2E suite runs
// two more in the same image -- mosquitto_pub and mosquitto_sub, which are how
// test/e2e proves the listener speaks MQTT and not merely TCP. Each of those is
// an assumption about a filesystem somebody else maintains and can change between
// tags.
//
// This tier exists because no other one can answer the question. Unit tests never
// leave the process, and integration runs envtest, which has no kubelet and
// therefore no container at all. Only a real image can say what is in a real
// image.
//
// It needs docker and no cluster, which is why it has its own build tag and its
// own CI job: it answers in seconds and names the missing binary, instead of
// surfacing minutes later as a StatefulSet that will not converge.
package imagetools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/builder"
	"github.com/guided-traffic/mosquitto-operator/test/testimages"
)

// imageProbeTimeout covers a cold pull of the image plus the probe itself.
const imageProbeTimeout = 5 * time.Minute

// clientTools are the Mosquitto client binaries the E2E suite executes inside a
// broker pod (test/e2e/mosquitto_test.go, test/e2e/tls_test.go). They are not
// used by the operator, but a tag that dropped them would take the reachability
// assertion of the whole E2E tier with it.
var clientTools = []string{"mosquitto_pub", "mosquitto_sub"}

// runInImage executes a shell snippet inside the image and returns what the
// script wrote to stdout.
//
// --entrypoint sh is required: the image starts the broker by default, which
// would ignore the script and hang.
//
// stdout and stderr are kept apart deliberately, and this is not hygiene. docker
// writes the progress of a cold pull to stderr, so a combined capture returns the
// whole layer list with the answer appended -- green on a runner that already
// holds the image, red on the first run of a fresh one. Only what the script
// echoed is the answer; stderr is kept for the failure message, where a real
// docker error has to stay readable.
func runInImage(t *testing.T, image, script string) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), imageProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--entrypoint", "sh", image, "-c", script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	require.NoError(t, err, "docker run against %s failed:\nstdout:\n%s\nstderr:\n%s",
		image, stdout.String(), stderr.String())
	return strings.TrimSpace(stdout.String())
}

// brokerCommand returns the command the operator puts in the broker container.
//
// It is read from the builder rather than written out here, so the check follows
// a change to that command instead of quietly continuing to probe the old path.
func brokerCommand(t *testing.T) []string {
	t.Helper()

	sts, err := builder.BuildStatefulSet(&mkov1.Mosquitto{
		ObjectMeta: metav1.ObjectMeta{Name: "probe", Namespace: "probe"},
		Spec:       mkov1.MosquittoSpec{Replicas: 1},
	})
	require.NoError(t, err, "the builder could not produce a StatefulSet to read the command from")
	require.Len(t, sts.Spec.Template.Spec.Containers, 1)

	command := sts.Spec.Template.Spec.Containers[0].Command
	require.NotEmpty(t, command, "the broker container names no command")
	return command
}

// TestImageProvidesEveryExecutedTool is the check the whole package exists for.
//
// It asks the image rather than the tag, so a rebase that drops a binary is
// caught on the Renovate pull request that introduces it -- the only moment where
// the answer is still cheap.
func TestImageProvidesEveryExecutedTool(t *testing.T) {
	t.Parallel()

	required := append([]string{brokerCommand(t)[0]}, clientTools...)

	// One `command -v` per tool, reported in a single run, so a missing image is
	// one pull rather than one per tool. `command -v` accepts an absolute path
	// too and answers whether it is executable.
	script := fmt.Sprintf(
		`missing=""; for tool in %s; do command -v "$tool" >/dev/null 2>&1 || missing="$missing $tool"; done; `+
			`[ -z "$missing" ] && echo OK || echo "MISSING:$missing"`,
		strings.Join(required, " "))

	assert.Equal(t, "OK", runInImage(t, testimages.MosquittoImage, script),
		"%s does not provide every binary this repository executes inside it; the failure would "+
			"otherwise appear inside the container, where it is hardest to read", testimages.MosquittoImage)
}

// TestPinnedImageIsTheOperatorDefault guards the assumption that makes this job
// meaningful. The pin lives twice -- in internal/builder as the image the
// operator gives a CR that names none, and in test/testimages as the image the
// suites provision -- and Renovate updates both from their own comment. If the
// two ever drift, this job checks an image no broker actually runs.
func TestPinnedImageIsTheOperatorDefault(t *testing.T) {
	t.Parallel()

	assert.Equal(t, builder.DefaultImage, testimages.MosquittoImage,
		"the tested pin and the operator default must be the same image")
}
