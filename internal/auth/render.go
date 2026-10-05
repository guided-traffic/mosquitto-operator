package auth

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
)

// ReservedPrefix is the username prefix kept for principals the operator
// renders itself, compared case-insensitively (ADR 0013 D5, ADR 0002 D4).
const ReservedPrefix = "mko-"

// ExporterUsername is the principal of the metrics exporter: rendered by the
// operator with read access to $SYS/# and nothing else (ADR 0002 D4).
const ExporterUsername = ReservedPrefix + "exporter"

// ExporterPrincipal is the exporter's principal with its one ACL entry.
func ExporterPrincipal(password string) Principal {
	return Principal{
		Username: ExporterUsername,
		Password: password,
		ACLs:     []mkov1.MosquittoACL{{Topic: "$SYS/#", Access: mkov1.AccessRead}},
	}
}

// Principal is a user the operator renders for itself under the reserved
// prefix. It bypasses the checks a MosquittoUser passes - the prefix and the $
// refusal exist to keep exactly these names and topics for it - and is never
// counted as a user.
type Principal struct {
	Username string
	Password string
	ACLs     []mkov1.MosquittoACL
}

// usernamePattern is the username allowlist of ADR 0013 D5: a character nobody
// thought of is refused rather than written into a file whose format uses ":"
// and line breaks, and +, # and / can never reach a later %u pattern.
var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,127}$`)

// Input is one MosquittoUser as the renderer sees it: the object's identity,
// which orders collisions, and what its credentials Secret held.
type Input struct {
	// Name is the MosquittoUser's object name, the key of its Verdict.
	Name string
	// CreationTimestamp decides a username collision: the oldest user wins.
	CreationTimestamp time.Time
	Username          string
	Password          string
	ACLs              []mkov1.MosquittoACL
}

// Accepted is a user the renderer admitted: its username, the hash it is
// rendered with, and its ACL entries, sorted and without duplicates.
type Accepted struct {
	Username string
	Hash     string
	ACLs     []mkov1.MosquittoACL
}

// Verdict is the renderer's answer for one Input: accepted, or the reason and
// the message of the user's Ready=False condition.
type Verdict struct {
	Accepted bool
	Reason   string
	Message  string
}

// Payload is the mode-specific half of rendering (ADR 0014 D9): the file
// plugins' two files today, a dynamic-security JSON document later. The
// mode-independent half - the checks, the collisions, the hashes - is Render.
type Payload interface {
	// Hashes returns the hash per username that a previously rendered payload
	// holds, so an unchanged password keeps its hash and salt.
	Hashes(previous map[string][]byte) map[string]string
	// Render returns the Secret data for the accepted users, which arrive
	// sorted by username. Equal input gives byte-equal output.
	Render(users []Accepted) map[string][]byte
}

// Result is what one pass renders for one broker.
type Result struct {
	// Data is the content of the broker's rendered Secret.
	Data map[string][]byte
	// Verdicts holds one entry per Input, keyed by object name.
	Verdicts map[string]Verdict
	// Accepted is how many users Data holds.
	Accepted int
}

// Render turns the users bound to one broker into the broker's credentials
// (ADR 0013 D3-D6, D8; ADR 0014 D3). It checks every input, resolves username
// collisions by age, keeps an existing hash while its plaintext still verifies,
// hashes the rest with random, and hands the accepted users to payload sorted by
// username, so the same users always render the same bytes whatever order they
// arrive in. previous is the data of the broker's current rendered Secret.
// reserved are the operator's own principals, rendered next to the users.
func Render(payload Payload, inputs []Input, reserved []Principal, previous map[string][]byte, random io.Reader) (Result, error) {
	result := Result{Verdicts: make(map[string]Verdict, len(inputs))}

	var candidates []Input
	for _, in := range inputs {
		if verdict, refused := check(in); refused {
			result.Verdicts[in.Name] = verdict
			continue
		}
		candidates = append(candidates, in)
	}

	winners := resolveCollisions(candidates, result.Verdicts)

	known := payload.Hashes(previous)
	accepted := make([]Accepted, 0, len(winners)+len(reserved))
	for _, in := range winners {
		hash, err := keepOrHash(known[in.Username], in.Password, random)
		if err != nil {
			return Result{}, fmt.Errorf("hashing the password of MosquittoUser %s: %w", in.Name, err)
		}
		accepted = append(accepted, Accepted{Username: in.Username, Hash: hash, ACLs: canonicalACLs(in.ACLs)})
		result.Verdicts[in.Name] = Verdict{Accepted: true, Reason: mkov1.ReasonUserAccepted,
			Message: fmt.Sprintf("rendered into the broker's credentials as %q", in.Username)}
	}
	result.Accepted = len(accepted)

	for _, p := range reserved {
		if !strings.HasPrefix(p.Username, ReservedPrefix) || p.Password == "" {
			return Result{}, fmt.Errorf("the operator's principal %q needs the prefix %s and a password", p.Username, ReservedPrefix)
		}
		hash, err := keepOrHash(known[p.Username], p.Password, random)
		if err != nil {
			return Result{}, fmt.Errorf("hashing the password of %s: %w", p.Username, err)
		}
		accepted = append(accepted, Accepted{Username: p.Username, Hash: hash, ACLs: canonicalACLs(p.ACLs)})
	}

	sort.Slice(accepted, func(i, j int) bool { return accepted[i].Username < accepted[j].Username })
	result.Data = payload.Render(accepted)
	return result, nil
}

// keepOrHash keeps a previous hash while the password still verifies against
// it, so an unchanged password renders the same bytes, and hashes it otherwise.
func keepOrHash(previous, password string, random io.Reader) (string, error) {
	if previous != "" && VerifyPassword(previous, password) {
		return previous, nil
	}
	return HashPassword(password, random)
}

// check applies the render-time rules to one input: the username allowlist,
// the reserved prefix, a non-empty password and every ACL topic. It reports
// the verdict and whether the input is refused.
func check(in Input) (Verdict, bool) {
	switch {
	case !usernamePattern.MatchString(in.Username):
		return refused(mkov1.ReasonUsernameInvalid, "username %q is not allowed: it must match %s",
			in.Username, usernamePattern.String()), true
	case strings.HasPrefix(strings.ToLower(in.Username), ReservedPrefix):
		return refused(mkov1.ReasonUsernameReserved,
			"username %q is reserved: the prefix %s belongs to principals the operator renders itself",
			in.Username, ReservedPrefix), true
	case in.Password == "":
		return refused(mkov1.ReasonPasswordEmpty, "the password is empty"), true
	}
	for _, acl := range in.ACLs {
		if problem := topicProblem(acl.Topic); problem != "" {
			return refused(mkov1.ReasonTopicRefused, "ACL topic %q is refused: %s", acl.Topic, problem), true
		}
		if acl.Access != mkov1.AccessRead && acl.Access != mkov1.AccessWrite && acl.Access != mkov1.AccessReadWrite {
			return refused(mkov1.ReasonTopicRefused, "ACL topic %q has access %q, which is not read, write or readwrite",
				acl.Topic, acl.Access), true
		}
	}
	return Verdict{}, false
}

// topicProblem returns why a topic filter cannot be rendered, or "". The CRD
// refuses the first two already; this is the authority, because a schema can
// be older than the operator or bypassed (ADR 0013 D4).
func topicProblem(topic string) string {
	switch {
	case topic == "":
		return "it is empty"
	case strings.HasPrefix(topic, "$"):
		return "a topic starting with $ is reserved for the broker and the operator"
	case !utf8.ValidString(topic):
		return "it is not valid UTF-8"
	case strings.IndexFunc(topic, isControl) >= 0:
		return "it contains a control character"
	case strings.TrimSpace(topic) != topic:
		return "it starts or ends with whitespace"
	}
	levels := strings.Split(topic, "/")
	for i, level := range levels {
		if strings.Contains(level, "#") && (level != "#" || i != len(levels)-1) {
			return "# must be a whole level and the last one"
		}
		if strings.Contains(level, "+") && level != "+" {
			return "+ must be a whole level"
		}
	}
	return ""
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// resolveCollisions keeps, per username, the oldest input - by creation time,
// then by object name - and records a UsernameConflict for every other one
// (ADR 0013 D6). It uses nothing but the inputs, no status as memory.
func resolveCollisions(candidates []Input, verdicts map[string]Verdict) []Input {
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if !a.CreationTimestamp.Equal(b.CreationTimestamp) {
			return a.CreationTimestamp.Before(b.CreationTimestamp)
		}
		return a.Name < b.Name
	})
	holders := make(map[string]string, len(candidates))
	winners := make([]Input, 0, len(candidates))
	for _, in := range candidates {
		if holder, taken := holders[in.Username]; taken {
			verdicts[in.Name] = refused(mkov1.ReasonUsernameConflict,
				"username %q is held by the older MosquittoUser %s", in.Username, holder)
			continue
		}
		holders[in.Username] = in.Name
		winners = append(winners, in)
	}
	return winners
}

// canonicalACLs returns the entries sorted by topic and access, without
// duplicates, so reordering a user's list in Git renders the same bytes.
func canonicalACLs(acls []mkov1.MosquittoACL) []mkov1.MosquittoACL {
	out := make([]mkov1.MosquittoACL, 0, len(acls))
	seen := make(map[mkov1.MosquittoACL]bool, len(acls))
	for _, acl := range acls {
		if !seen[acl] {
			seen[acl] = true
			out = append(out, acl)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Topic != out[j].Topic {
			return out[i].Topic < out[j].Topic
		}
		return out[i].Access < out[j].Access
	})
	return out
}

func refused(reason, format string, args ...any) Verdict {
	return Verdict{Reason: reason, Message: fmt.Sprintf(format, args...)}
}
