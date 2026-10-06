package reloader

import (
	"context"
	"flag"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// Run polls the Secret mount every interval, copies a change in and signals
// the broker, until ctx ends. With a TLS mount it also signals for a renewed
// certificate, after checking that it forms a valid pair with its key - and
// while the mounted pair is invalid it sends no signal at all, because a SIGHUP
// reloads the certificate too and an invalid pair would take the listener down
// (M12); a credential change then waits for a valid pair. A change that could
// not be signalled - the broker is starting, or restarting - is signalled on a
// later round, so it is never left half applied. Polling, not inotify: the
// kubelet publishes a change by swapping a symlink, about a minute after it
// happened (M23); a two-second poll adds nothing that matters to that.
func Run(ctx context.Context, cfg Config, interval time.Duration, logger *slog.Logger) {
	r := round{cfg: cfg, logger: logger}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		r.once()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// round is the state Run carries between polls.
type round struct {
	cfg     Config
	logger  *slog.Logger
	pending bool
	tls     tlsState
}

// once is one poll: the credentials, the TLS pair, then the signal.
func (r *round) once() {
	changed, err := Sync(r.cfg)
	switch {
	case err != nil:
		r.logger.Error("copying the credentials failed; the broker keeps the previous copy", "error", err)
	case changed:
		r.logger.Info("credentials changed, copied", "files", strings.Join(r.cfg.Files, ","))
		r.pending = true
	}

	if r.cfg.TLSDir != "" {
		tlsChanged, err := checkTLS(r.cfg.TLSDir, &r.tls)
		switch {
		case err != nil:
			r.logger.Error("the TLS pair is not valid; the broker keeps serving its previous certificate and is not signalled", "error", err)
		case tlsChanged:
			r.logger.Info("TLS certificate changed and checked")
			r.pending = true
		}
		if !r.tls.valid {
			return
		}
	}

	if r.pending {
		if err := r.cfg.signal(); err != nil {
			r.logger.Warn("could not signal the broker yet; retrying", "error", err)
			return
		}
		r.logger.Info("signalled the broker to reload", "process", r.cfg.Process)
		r.pending = false
	}
}

// Main is the entry point of "manager reload". With --once it copies and exits
// - the auth-init container, which runs before the broker on every start, so
// the Secret, not a copy left on a volume, is what the broker starts with. Without
// it, it is the reloader sidecar. It returns the process exit code.
func Main(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("reload", flag.ContinueOnError)
	fs.SetOutput(stderr)
	source := fs.String("source", "/mosquitto/auth-secret", "The mount of the rendered Secret.")
	target := fs.String("target", "/mosquitto/auth", "The directory the broker reads the copies from.")
	files := fs.String("files", "passwd,acl", "The keys to copy, comma-separated.")
	process := fs.String("process", "mosquitto", "The command name of the broker process.")
	tlsDir := fs.String("tls-dir", "", "The broker's TLS mount; a renewed, valid pair is signalled. Empty: no TLS.")
	once := fs.Bool("once", false, "Copy once and exit, without signalling: the auth-init container.")
	interval := fs.Duration("interval", 2*time.Second, "How often the Secret mount is compared with the copies.")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	logger := slog.New(slog.NewTextHandler(stderr, nil)).With("component", "reloader")
	cfg := Config{
		Source:   *source,
		Target:   *target,
		Files:    strings.Split(*files, ","),
		Process:  *process,
		ProcRoot: "/proc",
		TLSDir:   *tlsDir,
	}

	if *once {
		if _, err := Sync(cfg); err != nil {
			logger.Error("copying the credentials failed", "error", err)
			return 1
		}
		logger.Info("credentials copied", "target", cfg.Target)
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("watching the credentials", "source", cfg.Source, "target", cfg.Target, "interval", interval.String())
	Run(ctx, cfg, *interval, logger)
	return 0
}
