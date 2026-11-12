//go:build imagetools

package imagetools

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/builder"
	"github.com/guided-traffic/mosquitto-operator/test/testimages"
)

// brokerProbeCommand returns the command of the broker container's readiness
// probe, read from the builder like brokerCommand, and checks the liveness
// probe runs the same one.
func brokerProbeCommand(t *testing.T) []string {
	t.Helper()

	sts, err := builder.BuildStatefulSet(&mkov1.Mosquitto{
		ObjectMeta: metav1.ObjectMeta{Name: "probe", Namespace: "probe"},
		Spec:       mkov1.MosquittoSpec{Replicas: 1},
	}, builder.PodOptions{ReloaderImage: "probe"})
	require.NoError(t, err, "the builder could not produce a StatefulSet to read the probe from")

	for _, c := range sts.Spec.Template.Spec.Containers {
		if c.Name != builder.BrokerContainerName {
			continue
		}
		require.NotNil(t, c.ReadinessProbe, "the broker container has no readiness probe")
		require.NotNil(t, c.ReadinessProbe.Exec, "the readiness probe runs no command in the image")
		require.NotNil(t, c.LivenessProbe, "the broker container has no liveness probe")
		require.NotNil(t, c.LivenessProbe.Exec, "the liveness probe runs no command in the image")
		require.Equal(t, c.ReadinessProbe.Exec.Command, c.LivenessProbe.Exec.Command,
			"this test covers one probe command; a second one needs its own check")
		return c.ReadinessProbe.Exec.Command
	}
	require.Fail(t, "the StatefulSet has no broker container")
	return nil
}

// shellWords quotes a command for sh, one single-quoted word per argument.
func shellWords(command []string) string {
	words := make([]string, len(command))
	for i, w := range command {
		words[i] = "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
	}
	return strings.Join(words, " ")
}

// TestListenerProbeSeesTheBrokerWithoutConnecting is broker-behaviour.md M31,
// repeated on every pull request with the command the operator renders (ADR
// 0008 D17): against a listening broker in the pinned image it succeeds, it
// leaves no line in the broker log, and it fails for a port nobody listens on
// and once the broker has stopped. A probe that connected would log, and one
// that matched any socket would pass on a dead broker -- either turns red here.
func TestListenerProbeSeesTheBrokerWithoutConnecting(t *testing.T) {
	t.Parallel()

	probe := shellWords(brokerProbeCommand(t))
	otherPort := shellWords(builder.ListenerProbeCommand(builder.MQTTSPort))

	script := fmt.Sprintf(`set -e
mkdir -p /tmp/auth && cd /tmp/auth
: > passwd; : > acl
chmod 0600 passwd acl
cat > mosquitto.conf <<'CONF'
log_type error
log_type warning
log_type notice
log_type information
%sCONF
%sset +e
broker=$!
before=$(grep -c 'New connection' broker.log)
%s; echo "listening=$?"
i=0; while [ "$i" -lt 10 ]; do %s; i=$((i+1)); done
echo "logged=$(( $(grep -c 'New connection' broker.log) - before ))"
%s; echo "other_port=$?"
kill "$broker"; wait "$broker" 2>/dev/null
%s; echo "stopped=$?"
`, brokerWithPasswordFile, startBroker, probe, probe, otherPort, probe)

	out := runInImageAs(t, testimages.MosquittoImage, "1883:1883", script)
	assert.Contains(t, out, "listening=0", "the probe must pass while the broker listens:\n%s", out)
	assert.Contains(t, out, "logged=0", "the probe must not open a connection the broker logs:\n%s", out)
	assert.NotContains(t, out, "other_port=0", "the probe must fail for a port nobody listens on:\n%s", out)
	assert.NotContains(t, out, "stopped=0", "the probe must fail once the broker stopped:\n%s", out)
}
