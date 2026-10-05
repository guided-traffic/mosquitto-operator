package auth

import (
	"strings"
)

// The keys of the rendered Secret for the file plugins (ADR 0014 D2).
const (
	PasswdKey = "passwd"
	ACLKey    = "acl"
)

// FilePayload renders the two files the password-file and acl-file plugins
// read (ADR 0014 D1).
type FilePayload struct{}

// Hashes reads username:hash lines out of a previously rendered passwd file.
func (FilePayload) Hashes(previous map[string][]byte) map[string]string {
	hashes := make(map[string]string)
	for _, line := range strings.Split(string(previous[PasswdKey]), "\n") {
		if username, hash, found := strings.Cut(line, ":"); found && username != "" {
			hashes[username] = hash
		}
	}
	return hashes
}

// Render writes one username:hash line per user to passwd, and one user block
// with its topic lines per user to acl. A user without ACL entries gets a user
// line and nothing else, which grants nothing. Both files are empty, not
// missing, when there are no users: the plugins then accept nobody.
func (FilePayload) Render(users []Accepted) map[string][]byte {
	var passwd, acl strings.Builder
	for i, u := range users {
		passwd.WriteString(u.Username + ":" + u.Hash + "\n")
		if i > 0 {
			acl.WriteString("\n")
		}
		acl.WriteString("user " + u.Username + "\n")
		for _, entry := range u.ACLs {
			acl.WriteString("topic " + entry.Access + " " + entry.Topic + "\n")
		}
	}
	return map[string][]byte{
		PasswdKey: []byte(passwd.String()),
		ACLKey:    []byte(acl.String()),
	}
}
