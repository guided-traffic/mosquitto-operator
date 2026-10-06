package reloader

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// publish lays out a Secret volume the way the kubelet does: the files in a
// timestamped directory, ..data pointing at it, and each key a link through
// ..data. Calling it again swaps ..data in one rename (M23).
func publish(t *testing.T, mount, version string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(mount, "..v"+version)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
		link := filepath.Join(mount, name)
		if _, err := os.Lstat(link); os.IsNotExist(err) {
			require.NoError(t, os.Symlink(filepath.Join(dataLink, name), link))
		}
	}
	tmp := filepath.Join(mount, "..data_tmp")
	require.NoError(t, os.Symlink("..v"+version, tmp))
	require.NoError(t, os.Rename(tmp, filepath.Join(mount, dataLink)))
}

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		Source:  t.TempDir(),
		Target:  t.TempDir(),
		Files:   []string{"passwd", "acl"},
		Process: "mosquitto",
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func TestSync_CopiesOnceAndOnlyOnChange(t *testing.T) {
	cfg := testConfig(t)
	publish(t, cfg.Source, "1", map[string]string{"passwd": "alice:$7$a\n", "acl": "user alice\n"})

	changed, err := Sync(cfg)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "alice:$7$a\n", read(t, filepath.Join(cfg.Target, "passwd")))
	info, err := os.Stat(filepath.Join(cfg.Target, "passwd"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the broker accepts its files at 0600 without a warning (M6)")

	changed, err = Sync(cfg)
	require.NoError(t, err)
	assert.False(t, changed, "identical bytes are not copied again, so the broker is not signalled")

	publish(t, cfg.Source, "2", map[string]string{"passwd": "alice:$7$a\nbob:$7$b\n", "acl": "user alice\n"})
	changed, err = Sync(cfg)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "alice:$7$a\nbob:$7$b\n", read(t, filepath.Join(cfg.Target, "passwd")))

	entries, err := os.ReadDir(cfg.Target)
	require.NoError(t, err)
	assert.Len(t, entries, 2, "no temporary file is left behind")
}

func TestSync_ReadsAPlainDirectory(t *testing.T) {
	cfg := testConfig(t)
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Source, "passwd"), []byte("p"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Source, "acl"), []byte("a"), 0o644))

	changed, err := Sync(cfg)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "a", read(t, filepath.Join(cfg.Target, "acl")))
}

func TestSync_AMissingKeyLeavesTheCopiesAlone(t *testing.T) {
	cfg := testConfig(t)
	publish(t, cfg.Source, "1", map[string]string{"passwd": "old\n", "acl": "old\n"})
	_, err := Sync(cfg)
	require.NoError(t, err)

	publish(t, cfg.Source, "2", map[string]string{"passwd": "new\n"})
	_, err = Sync(cfg)
	assert.Error(t, err)
	assert.Equal(t, "old\n", read(t, filepath.Join(cfg.Target, "passwd")),
		"the set is read in full before anything is copied, so a broken Secret changes nothing")
}

func TestFindProcess(t *testing.T) {
	proc := t.TempDir()
	for pid, comm := range map[string]string{"1": "pause\n", "7": "mosquitto\n", "12": "manager\n", "self": "x\n"} {
		require.NoError(t, os.MkdirAll(filepath.Join(proc, pid), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(proc, pid, "comm"), []byte(comm), 0o644))
	}

	pid, err := FindProcess(proc, "mosquitto")
	require.NoError(t, err)
	assert.Equal(t, 7, pid)

	_, err = FindProcess(proc, "missing")
	assert.ErrorIs(t, err, errNoProcess)
	_, err = FindProcess(filepath.Join(proc, "absent"), "mosquitto")
	assert.Error(t, err)
}

// recorder counts the signals Run sends.
type recorder struct {
	mu   sync.Mutex
	pids []int
}

func (r *recorder) send(pid int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pids = append(r.pids, pid)
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pids)
}

// TestRun_SignalsOnChangeAndRetriesUntilTheBrokerIsThere: an unchanged mount
// sends nothing; a change is copied and signalled; a change made while no broker
// process exists is signalled once one does.
func TestRun_SignalsOnChangeAndRetriesUntilTheBrokerIsThere(t *testing.T) {
	cfg := testConfig(t)
	proc := t.TempDir()
	cfg.ProcRoot = proc
	rec := &recorder{}
	cfg.Signal = rec.send
	publish(t, cfg.Source, "1", map[string]string{"passwd": "a\n", "acl": "a\n"})
	_, err := Sync(cfg) // what auth-init did before the sidecar started
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, cfg, 10*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	defer func() { cancel(); <-done }()

	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, rec.count(), "nothing changed since auth-init, so nothing is signalled")

	publish(t, cfg.Source, "2", map[string]string{"passwd": "a\nb\n", "acl": "a\n"})
	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, rec.count(), "no broker process yet: the signal is pending, not lost")
	assert.Equal(t, "a\nb\n", read(t, filepath.Join(cfg.Target, "passwd")), "the copy happened anyway")

	require.NoError(t, os.MkdirAll(filepath.Join(proc, "7"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(proc, "7", "comm"), []byte("mosquitto\n"), 0o644))
	require.Eventually(t, func() bool { return rec.count() == 1 }, time.Second, 10*time.Millisecond)

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, rec.count(), "one change, one signal")
	assert.Equal(t, []int{7}, rec.pids)
}

func TestMain_Once(t *testing.T) {
	cfg := testConfig(t)
	publish(t, cfg.Source, "1", map[string]string{"passwd": "p\n", "acl": "a\n"})

	var stderr bytes.Buffer
	code := Main([]string{"--once", "--source", cfg.Source, "--target", cfg.Target}, &stderr)
	assert.Equal(t, 0, code, stderr.String())
	assert.Equal(t, "p\n", read(t, filepath.Join(cfg.Target, "passwd")))

	assert.Equal(t, 1, Main([]string{"--once", "--source", t.TempDir(), "--target", cfg.Target}, &stderr),
		"an empty mount fails auth-init, so the pod does not start without its credentials")
	assert.Equal(t, 2, Main([]string{"--no-such-flag"}, &stderr))
}
