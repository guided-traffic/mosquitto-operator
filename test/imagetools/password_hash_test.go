//go:build imagetools

package imagetools

import (
	"crypto/rand"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/auth"
	"github.com/guided-traffic/mosquitto-operator/test/testimages"
)

// brokerWithPasswordFile is the listener the operator generates for users
// (ADR 0008 D13, ADR 0014 D1): both file plugins, no anonymous access. The
// files live in /tmp of the probe container.
const brokerWithPasswordFile = `log_dest stdout
plugin_load pwfile /usr/lib/mosquitto_password_file.so
plugin_opt_password_file /tmp/auth/passwd
plugin_load aclfile /usr/lib/mosquitto_acl_file.so
plugin_opt_acl_file /tmp/auth/acl
listener 1883
listener_allow_anonymous false
plugin_use pwfile
plugin_use aclfile
`

// TestImageAcceptsTheHashTheOperatorRenders is broker-behaviour.md M20, repeated
// on every pull request: a $7$ line auth.HashPassword renders logs a client in
// with its password and refuses a wrong one, on the pinned image. A Renovate
// bump that changed the hash format or the plugin's verifier turns red here,
// not as every client of every broker refused.
func TestImageAcceptsTheHashTheOperatorRenders(t *testing.T) {
	t.Parallel()

	hash, err := auth.HashPassword("go-rendered-pw", rand.Reader)
	require.NoError(t, err)

	script := fmt.Sprintf(`set -e
mkdir -p /tmp/auth && cd /tmp/auth
printf '%%s\n' 'probe:%s' > passwd
printf 'user probe\ntopic readwrite probe/#\n' > acl
chmod 0600 passwd acl
cat > mosquitto.conf <<'CONF'
%sCONF
/usr/sbin/mosquitto -c mosquitto.conf > broker.log 2>&1 &
sleep 1
set +e
mosquitto_pub -h 127.0.0.1 -u probe -P go-rendered-pw -t probe/x -m hi >/dev/null 2>&1; echo "right=$?"
mosquitto_pub -h 127.0.0.1 -u probe -P wrong -t probe/x -m hi >/dev/null 2>&1; echo "wrong=$?"
`, hash, brokerWithPasswordFile)

	out := runInImageAs(t, testimages.MosquittoImage, "1883:1883", script)
	assert.Contains(t, out, "right=0", "the broker refused the password of a hash the operator rendered")
	assert.Contains(t, out, "wrong=5", "the broker must refuse a wrong password with not authorised (5)")
}

// TestOperatorVerifiesTheImagesHash is the other direction: a line
// mosquitto_passwd of the pinned image writes verifies with auth.VerifyPassword,
// which is what lets the renderer keep an existing hash while its plaintext
// still matches (ADR 0014 D3).
func TestOperatorVerifiesTheImagesHash(t *testing.T) {
	t.Parallel()

	line := runInImageAs(t, testimages.MosquittoImage, "1883:1883",
		`mosquitto_passwd -c -b /tmp/passwd probe image-pw >/dev/null 2>&1 && cat /tmp/passwd`)
	user, hash, found := strings.Cut(line, ":")
	require.True(t, found, "mosquitto_passwd wrote no user:hash line: %q", line)
	require.Equal(t, "probe", user)

	assert.True(t, auth.VerifyPassword(hash, "image-pw"), "the operator cannot verify the image's own hash %q", hash)
	assert.False(t, auth.VerifyPassword(hash, "other"))
}

// TestImageGrantsTheExporterItsSysTreeAndNothingElse renders the exporter's
// principal next to a user the way the operator does, and asks the pinned
// image what each may read (ADR 0002 D4, M13): mko-exporter reads $SYS and no
// application topic; a user granted # reads no $SYS topic.
func TestImageGrantsTheExporterItsSysTreeAndNothingElse(t *testing.T) {
	t.Parallel()

	result, err := auth.Render(auth.FilePayload{},
		[]auth.Input{{Name: "probe", Username: "probe", Password: "probe-pw",
			ACLs: []mkov1.MosquittoACL{{Topic: "#", Access: mkov1.AccessReadWrite}}}},
		[]auth.Principal{auth.ExporterPrincipal("exporter-pw")}, nil, rand.Reader)
	require.NoError(t, err)

	script := fmt.Sprintf(`set -e
mkdir -p /tmp/auth && cd /tmp/auth
cat > passwd <<'PW'
%sPW
cat > acl <<'ACL'
%sACL
chmod 0600 passwd acl
cat > mosquitto.conf <<'CONF'
%sCONF
/usr/sbin/mosquitto -c mosquitto.conf > broker.log 2>&1 &
sleep 1
mosquitto_pub -h 127.0.0.1 -u probe -P probe-pw -r -t app/state -m on
set +e
echo "exporter-sys=$(mosquitto_sub -h 127.0.0.1 -u mko-exporter -P exporter-pw -t '$SYS/broker/version' -C 1 -W 5)"
echo "exporter-app=$(mosquitto_sub -h 127.0.0.1 -u mko-exporter -P exporter-pw -t 'app/#' -C 1 -W 3 2>&1)"
echo "user-sys=$(mosquitto_sub -h 127.0.0.1 -u probe -P probe-pw -t '$SYS/broker/version' -C 1 -W 3 2>&1)"
`, result.Data[auth.PasswdKey], result.Data[auth.ACLKey], brokerWithPasswordFile)

	out := runInImageAs(t, testimages.MosquittoImage, "1883:1883", script)
	assert.Contains(t, out, "exporter-sys=mosquitto version", "mko-exporter must read $SYS")
	assert.Contains(t, out, "exporter-app=Timed out", "mko-exporter must read nothing else")
	assert.Contains(t, out, "user-sys=Timed out", "a user granted # must not read $SYS")
}
