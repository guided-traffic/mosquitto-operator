// Package reloader is the second entry point of the operator binary: it runs
// inside every broker pod, as the auth-init container once and as the reloader
// sidecar for the pod's lifetime (ADR 0014 D4-D6). It copies the credentials
// the operator renders from the mounted Secret into the directory the broker
// reads, and sends the broker SIGHUP when they change, so a new user, a changed
// password or a removed one reaches the running broker without a restart.
package reloader

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// dataLink is the symlink through which the kubelet publishes a Secret volume:
// every key is a link through it, and it is swapped in one rename when the
// Secret changes (docs/developer/broker-behaviour.md M23).
const dataLink = "..data"

// Config is what one reloader copies and whom it signals.
type Config struct {
	// Source is the mount of the rendered Secret.
	Source string
	// Target is the emptyDir the broker reads the copies from.
	Target string
	// Files are the keys copied from Source to Target.
	Files []string
	// Process is the command name (/proc/<pid>/comm) of the broker.
	Process string
	// ProcRoot is where processes are looked up; /proc in a pod.
	ProcRoot string
	// Signal delivers SIGHUP to a pid; nil means syscall.Kill.
	Signal func(pid int) error
	// TLSDir is the broker's TLS mount, or "" without TLS. A SIGHUP reloads
	// the certificate too, so the reloader signals for a renewal and never
	// while the mounted pair is invalid (ADR 0001 D10, ADR 0014 D5).
	TLSDir string
}

// resolveData returns the directory a Secret mount's files are read from: the
// target of its ..data link, which the kubelet swaps in one rename, or the
// directory itself when it has none.
func resolveData(dir string) string {
	target, err := os.Readlink(filepath.Join(dir, dataLink))
	if err != nil {
		return dir
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}
	return target
}

// readSource reads every file of the Secret mount through one resolved
// ..data target, so the set is taken from a single published version even when
// the kubelet swaps the volume in between two reads. A directory without
// ..data - a plain directory in a test - is read as it is.
func readSource(cfg Config) (map[string][]byte, error) {
	dir := resolveData(cfg.Source)
	files := make(map[string][]byte, len(cfg.Files))
	for _, name := range cfg.Files {
		data, err := os.ReadFile(filepath.Join(dir, name)) // #nosec G304 -- a fixed key under the pod's own Secret mount
		if err != nil {
			return nil, fmt.Errorf("reading %s from the Secret mount: %w", name, err)
		}
		files[name] = data
	}
	return files, nil
}

// Sync copies every file whose bytes differ from the copy in Target, by writing
// a temporary file next to it and renaming it over the copy, so the broker never
// reads half a file. The copies are mode 0600, owned by the process's uid and
// group - 1883:1883 in a broker pod, the state the broker accepts without a
// warning (M6). It reports whether any copy changed.
func Sync(cfg Config) (bool, error) {
	files, err := readSource(cfg)
	if err != nil {
		return false, err
	}
	changed := false
	for _, name := range cfg.Files {
		current, err := os.ReadFile(filepath.Join(cfg.Target, name)) // #nosec G304 -- a fixed key under the pod's own emptyDir
		if err == nil && bytes.Equal(current, files[name]) {
			continue
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return changed, fmt.Errorf("reading the copy of %s: %w", name, err)
		}
		if err := writeAtomic(cfg.Target, name, files[name]); err != nil {
			return changed, err
		}
		changed = true
	}
	return changed, nil
}

// writeAtomic replaces dir/name with data through a temporary file in the same
// directory and a rename.
func writeAtomic(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating a temporary copy of %s: %w", name, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing the copy of %s: %w", name, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setting the mode of the copy of %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing the copy of %s: %w", name, err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("renaming the copy of %s into place: %w", name, err)
	}
	return nil
}

// errNoProcess is FindProcess's answer when no process has the name.
var errNoProcess = errors.New("no such process")

// FindProcess returns the pid of a process whose command name is name. In a
// pod with shareProcessNamespace the broker's process is visible here, and
// /proc/<pid>/comm reads "mosquitto" (M22).
func FindProcess(procRoot, name string) (int, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return 0, fmt.Errorf("listing %s: %w", procRoot, err)
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		comm, err := os.ReadFile(filepath.Join(procRoot, entry.Name(), "comm")) // #nosec G304 -- /proc/<pid>/comm
		if err == nil && strings.TrimSpace(string(comm)) == name {
			return pid, nil
		}
	}
	return 0, fmt.Errorf("%w: %s", errNoProcess, name)
}

// signal sends SIGHUP to the broker. The sender shares the broker's uid and
// holds no capability; the kernel allows that and nothing more (M22).
func (cfg Config) signal() error {
	pid, err := FindProcess(cfg.ProcRoot, cfg.Process)
	if err != nil {
		return err
	}
	send := cfg.Signal
	if send == nil {
		send = func(pid int) error { return syscall.Kill(pid, syscall.SIGHUP) }
	}
	if err := send(pid); err != nil {
		return fmt.Errorf("signalling %s (pid %d): %w", cfg.Process, pid, err)
	}
	return nil
}
