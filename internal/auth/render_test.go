package auth

import (
	"bytes"
	"crypto/rand"
	"fmt"
	mathrand "math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
)

var epoch = time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)

func input(name, username, password string, age time.Duration, acls ...mkov1.MosquittoACL) Input {
	return Input{Name: name, Username: username, Password: password, CreationTimestamp: epoch.Add(-age), ACLs: acls}
}

func acl(topic, access string) mkov1.MosquittoACL {
	return mkov1.MosquittoACL{Topic: topic, Access: access}
}

func mustRender(t *testing.T, inputs []Input, previous map[string][]byte) Result {
	t.Helper()
	result, err := Render(FilePayload{}, inputs, nil, previous, rand.Reader)
	require.NoError(t, err)
	return result
}

func migrationUsers() []Input {
	return []Input{
		input("homeassistant", "homeassistant", "ha-pw", 3*time.Hour,
			acl("homeassistant/#", mkov1.AccessReadWrite), acl("zigbee2mqtt/#", mkov1.AccessRead)),
		input("zigbee2mqtt", "Zigbee_2MQTT", "z2m-pw", 2*time.Hour,
			acl("zigbee2mqtt/#", mkov1.AccessReadWrite), acl("homeassistant/status", mkov1.AccessRead)),
		input("sensor", "sensor@garden.lan", "s-pw", time.Hour, acl("garden/+/temperature", mkov1.AccessWrite)),
		input("nobody", "idle", "i-pw", time.Minute),
	}
}

// TestRender_IsDeterministic is ADR 0013 D8: one set of logins rendered a
// hundred times gives one output, in shuffled order and with the objects renamed
// and recreated - new names, new creation times - so a pass that changes no
// login signals the broker nothing. The hashes come from the first render, as
// they do from the broker's current Secret in the reconciler.
func TestRender_IsDeterministic(t *testing.T) {
	first := mustRender(t, migrationUsers(), nil)
	rng := mathrand.New(mathrand.NewPCG(1, 2))

	for i := 0; i < 100; i++ {
		users := migrationUsers()
		for u := range users {
			users[u].Name = fmt.Sprintf("renamed-%d-%d", i, rng.IntN(1000))
			users[u].CreationTimestamp = epoch.Add(time.Duration(rng.IntN(10000)) * time.Second)
		}
		rng.Shuffle(len(users), func(a, b int) { users[a], users[b] = users[b], users[a] })
		for u := range users {
			acls := users[u].ACLs
			rng.Shuffle(len(acls), func(a, b int) { acls[a], acls[b] = acls[b], acls[a] })
		}
		again := mustRender(t, users, first.Data)
		require.Equal(t, string(first.Data[PasswdKey]), string(again.Data[PasswdKey]), "render %d", i)
		require.Equal(t, string(first.Data[ACLKey]), string(again.Data[ACLKey]), "render %d", i)
	}
}

func TestRender_TheFiles(t *testing.T) {
	result := mustRender(t, migrationUsers(), nil)
	assert.Equal(t, 4, result.Accepted)

	lines := strings.Split(strings.TrimSuffix(string(result.Data[PasswdKey]), "\n"), "\n")
	require.Len(t, lines, 4)
	for i, username := range []string{"Zigbee_2MQTT", "homeassistant", "idle", "sensor@garden.lan"} {
		assert.True(t, strings.HasPrefix(lines[i], username+":$7$1000$"), "line %d: %q", i, lines[i])
	}

	assert.Equal(t, `user Zigbee_2MQTT
topic read homeassistant/status
topic readwrite zigbee2mqtt/#

user homeassistant
topic readwrite homeassistant/#
topic read zigbee2mqtt/#

user idle

user sensor@garden.lan
topic write garden/+/temperature
`, string(result.Data[ACLKey]))
}

func TestRender_NoUsersRendersEmptyFiles(t *testing.T) {
	result := mustRender(t, nil, nil)
	assert.Equal(t, 0, result.Accepted)
	assert.Equal(t, map[string][]byte{PasswdKey: []byte(""), ACLKey: []byte("")}, result.Data,
		"both keys exist and are empty: the plugins load and accept nobody")
}

// TestRender_KeepsAHashWhileItsPasswordVerifies is ADR 0014 D3: a fresh salt on
// every pass would change the Secret on every pass and signal the broker forever.
func TestRender_KeepsAHashWhileItsPasswordVerifies(t *testing.T) {
	first := mustRender(t, migrationUsers(), nil)

	unchanged := mustRender(t, migrationUsers(), first.Data)
	assert.Equal(t, first.Data, unchanged.Data)

	users := migrationUsers()
	users[2].Password = "rotated"
	rotated := mustRender(t, users, first.Data)
	before, after := FilePayload{}.Hashes(first.Data), FilePayload{}.Hashes(rotated.Data)
	assert.NotEqual(t, before["sensor@garden.lan"], after["sensor@garden.lan"], "a changed password gets a new hash")
	assert.True(t, VerifyPassword(after["sensor@garden.lan"], "rotated"))
	assert.Equal(t, before["homeassistant"], after["homeassistant"], "the other users keep theirs")
}

func TestRender_Refusals(t *testing.T) {
	tests := []struct {
		name       string
		in         Input
		wantReason string
	}{
		{"a colon would split the passwd line", input("u", "a:b", "pw", 0), mkov1.ReasonUsernameInvalid},
		{"a line break would inject a user", input("u", "a\nb", "pw", 0), mkov1.ReasonUsernameInvalid},
		{"a trailing newline from echo into a file", input("u", "alice\n", "pw", 0), mkov1.ReasonUsernameInvalid},
		{"a space", input("u", "a b", "pw", 0), mkov1.ReasonUsernameInvalid},
		{"a + that a later %u pattern would read as a wildcard", input("u", "a+b", "pw", 0), mkov1.ReasonUsernameInvalid},
		{"a leading dot", input("u", ".alice", "pw", 0), mkov1.ReasonUsernameInvalid},
		{"129 characters", input("u", strings.Repeat("a", 129), "pw", 0), mkov1.ReasonUsernameInvalid},
		{"an empty username", input("u", "", "pw", 0), mkov1.ReasonUsernameInvalid},
		{"the reserved prefix", input("u", "mko-exporter", "pw", 0), mkov1.ReasonUsernameReserved},
		{"the reserved prefix in another case", input("u", "MKO-Exporter", "pw", 0), mkov1.ReasonUsernameReserved},
		{"an empty password", input("u", "alice", "", 0), mkov1.ReasonPasswordEmpty},
		{"$SYS although CEL was bypassed", input("u", "alice", "pw", 0, acl("$SYS/#", mkov1.AccessRead)), mkov1.ReasonTopicRefused},
		{"$CONTROL", input("u", "alice", "pw", 0, acl("$CONTROL/dynamic-security/v1", mkov1.AccessWrite)), mkov1.ReasonTopicRefused},
		{"a line break in a topic", input("u", "alice", "pw", 0, acl("a\ntopic readwrite #", mkov1.AccessRead)), mkov1.ReasonTopicRefused},
		{"a leading space", input("u", "alice", "pw", 0, acl(" home/#", mkov1.AccessRead)), mkov1.ReasonTopicRefused},
		{"# not as the last level", input("u", "alice", "pw", 0, acl("home/#/x", mkov1.AccessRead)), mkov1.ReasonTopicRefused},
		{"# inside a level", input("u", "alice", "pw", 0, acl("home#", mkov1.AccessRead)), mkov1.ReasonTopicRefused},
		{"+ inside a level", input("u", "alice", "pw", 0, acl("home/a+/x", mkov1.AccessRead)), mkov1.ReasonTopicRefused},
		{"an empty topic", input("u", "alice", "pw", 0, acl("", mkov1.AccessRead)), mkov1.ReasonTopicRefused},
		{"an access mode the CRD would refuse", input("u", "alice", "pw", 0, acl("home/#", "deny")), mkov1.ReasonTopicRefused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mustRender(t, []Input{tt.in}, nil)
			verdict := result.Verdicts["u"]
			assert.False(t, verdict.Accepted)
			assert.Equal(t, tt.wantReason, verdict.Reason, verdict.Message)
			assert.Equal(t, 0, result.Accepted)
			assert.Empty(t, result.Data[PasswdKey], "a refused user is never rendered, never silently trimmed")
		})
	}
}

func TestRender_AcceptsWhatTheAllowlistAllows(t *testing.T) {
	for _, username := range []string{"a", "Zigbee_2MQTT", "ha@home.lan", "z2m.bridge", "x-1", strings.Repeat("a", 128), "mko"} {
		result := mustRender(t, []Input{input("u", username, "pw", 0, acl("+/#", mkov1.AccessRead), acl("#", mkov1.AccessWrite))}, nil)
		assert.True(t, result.Verdicts["u"].Accepted, "%q: %s", username, result.Verdicts["u"].Message)
	}
}

// TestRender_TheOldestUserKeepsTheUsername is ADR 0013 D6: a new object can
// never take over a running client's identity.
func TestRender_TheOldestUserKeepsTheUsername(t *testing.T) {
	result := mustRender(t, []Input{
		input("newcomer", "z2m", "evil", time.Minute),
		input("original", "z2m", "good", time.Hour),
		input("other", "ha", "pw", time.Second),
	}, nil)

	assert.True(t, result.Verdicts["original"].Accepted)
	assert.Equal(t, mkov1.ReasonUsernameConflict, result.Verdicts["newcomer"].Reason)
	assert.Contains(t, result.Verdicts["newcomer"].Message, "original", "the message names the holder")
	assert.True(t, VerifyPassword(FilePayload{}.Hashes(result.Data)["z2m"], "good"))
	assert.Equal(t, 2, result.Accepted)

	tie := mustRender(t, []Input{input("b", "same", "pw-b", 0), input("a", "same", "pw-a", 0)}, nil)
	assert.True(t, tie.Verdicts["a"].Accepted, "the same creation time falls back to the object name")
	assert.Equal(t, mkov1.ReasonUsernameConflict, tie.Verdicts["b"].Reason)
}

func TestRender_FailsWhenNoSaltCanBeRead(t *testing.T) {
	_, err := Render(FilePayload{}, migrationUsers(), nil, nil, bytes.NewReader(nil))
	assert.Error(t, err)
}

func TestFilePayload_Hashes(t *testing.T) {
	hashes := FilePayload{}.Hashes(map[string][]byte{PasswdKey: []byte("a:$7$x\nb:$7$y\n\nbroken\n")})
	assert.Equal(t, map[string]string{"a": "$7$x", "b": "$7$y"}, hashes)
	assert.Empty(t, FilePayload{}.Hashes(nil))
}

// TestRender_TheExporterPrincipal is ADR 0002 D4: the operator renders its own
// exporter user next to the MosquittoUser objects - under the reserved prefix
// no MosquittoUser can claim, with the $SYS grant no MosquittoUser can hold -
// keeps its hash while the password stays, and never counts it as a user.
func TestRender_TheExporterPrincipal(t *testing.T) {
	users := []Input{
		input("ha", "homeassistant", "pw", 0, acl("homeassistant/#", mkov1.AccessReadWrite)),
		input("claim", "mko-exporter", "stolen", time.Hour, acl("$SYS/#", mkov1.AccessRead)),
	}
	render := func(previous map[string][]byte) Result {
		t.Helper()
		result, err := Render(FilePayload{}, users, []Principal{ExporterPrincipal("exp-pw")}, previous, rand.Reader)
		require.NoError(t, err)
		return result
	}

	first := render(nil)
	assert.Equal(t, 1, first.Accepted, "the exporter is not a user")
	assert.Equal(t, mkov1.ReasonUsernameReserved, first.Verdicts["claim"].Reason, "a MosquittoUser still cannot claim the name")
	hashes := FilePayload{}.Hashes(first.Data)
	assert.True(t, VerifyPassword(hashes[ExporterUsername], "exp-pw"), "the operator's password, not the claimant's")
	assert.Equal(t, "user homeassistant\ntopic readwrite homeassistant/#\n\nuser mko-exporter\ntopic read $SYS/#\n",
		string(first.Data[ACLKey]))

	assert.Equal(t, first.Data, render(first.Data).Data, "an unchanged exporter password keeps its hash")

	_, err := Render(FilePayload{}, nil, []Principal{{Username: "exporter", Password: "pw"}}, nil, rand.Reader)
	assert.ErrorContains(t, err, "needs the prefix", "the bypass is for the reserved prefix only")
}
