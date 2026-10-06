// Package exporter is the broker metrics exporter of ADR 0002: an MQTT session
// with the broker of its own pod as the reserved user mko-exporter, the $SYS
// tree mapped to Prometheus series, and /metrics over plain HTTP.
package exporter

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Config is what one exporter needs: its broker, its credential, the
// certificate it pins under TLS, and where it serves /metrics.
type Config struct {
	// Broker is the MQTT URL of the pod's own broker: tcp://127.0.0.1:1883,
	// or ssl://127.0.0.1:8883 under TLS.
	Broker string
	// Username is the reserved principal, mko-exporter.
	Username string
	// PasswordFile holds the password the operator generated; it is read on
	// every connection attempt.
	PasswordFile string
	// TLSCert is the broker's mounted certificate. Under TLS the exporter
	// accepts exactly the certificate in this file, read on every handshake:
	// the connection never leaves the pod, and the certificate names the
	// broker's DNS names, not 127.0.0.1.
	TLSCert string
	// Listen is the address /metrics is served on.
	Listen string
}

// subscription is the one topic filter the exporter's ACL grants.
const subscription = "$SYS/broker/#"

// newClient builds the MQTT client: the client ID is the username, which the
// broker enforces anyway (ADR 0008 D14); a lost session drops every value and
// the client reconnects on its own.
func newClient(cfg Config, collector *Collector, logger *slog.Logger) mqtt.Client {
	opts := mqtt.NewClientOptions().
		AddBroker(cfg.Broker).
		SetClientID(cfg.Username).
		SetCredentialsProvider(func() (string, string) {
			password, err := os.ReadFile(cfg.PasswordFile)
			if err != nil {
				logger.Error("reading the password", "error", err)
			}
			return cfg.Username, strings.TrimSpace(string(password))
		}).
		SetCleanSession(true).
		SetKeepAlive(30 * time.Second).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetAutoReconnect(true).
		SetMaxReconnectInterval(30 * time.Second).
		SetOnConnectHandler(func(c mqtt.Client) {
			token := c.Subscribe(subscription, 0, func(_ mqtt.Client, msg mqtt.Message) {
				collector.Observe(msg.Topic(), msg.Payload())
			})
			if token.Wait() && token.Error() != nil {
				logger.Error("subscribing", "topic", subscription, "error", token.Error())
				return
			}
			collector.Connected()
			logger.Info("connected and subscribed", "broker", cfg.Broker, "topic", subscription)
		}).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			collector.Lost()
			logger.Warn("connection lost; the broker series are absent until it is back", "error", err)
		})
	if cfg.TLSCert != "" {
		opts.SetTLSConfig(pinnedTLS(cfg.TLSCert))
	}
	return mqtt.NewClient(opts)
}

// pinnedTLS accepts exactly the certificate in path, re-read on every
// handshake, so a renewed certificate is accepted once the broker serves it.
func pinnedTLS(path string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// The chain and the host name are not what is checked: the peer is
		// 127.0.0.1 inside the pod, and VerifyConnection pins the certificate
		// the broker was given.
		InsecureSkipVerify: true, // #nosec G402 -- replaced by the pin in VerifyConnection
		VerifyConnection: func(cs tls.ConnectionState) error {
			pinned, err := firstCertificate(path)
			if err != nil {
				return err
			}
			if len(cs.PeerCertificates) == 0 || !bytes.Equal(cs.PeerCertificates[0].Raw, pinned.Raw) {
				return errors.New("the broker serves a certificate other than the mounted one")
			}
			return nil
		},
	}
}

func firstCertificate(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the pod's own TLS mount, from a flag the operator sets
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("%s holds no PEM block", path)
	}
	return x509.ParseCertificate(block.Bytes)
}

// Handler serves the collector, and nothing else, as /metrics.
func Handler(collector *Collector) http.Handler {
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	return mux
}

// Run connects, serves /metrics on listener and returns when ctx ends.
func Run(ctx context.Context, cfg Config, listener net.Listener, logger *slog.Logger) error {
	collector := NewCollector()
	client := newClient(cfg, collector, logger)
	client.Connect() // with ConnectRetry the token completes only once connected; nothing waits on it
	defer client.Disconnect(250)

	server := &http.Server{Handler: Handler(collector), ReadHeaderTimeout: 10 * time.Second}
	errs := make(chan error, 1)
	go func() { errs <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	case err := <-errs:
		return err
	}
}

// Main is the entry point of the exporter binary.
func Main(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("exporter", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := Config{}
	fs.StringVar(&cfg.Broker, "broker", "tcp://127.0.0.1:1883", "The MQTT URL of the pod's own broker.")
	fs.StringVar(&cfg.Username, "username", "mko-exporter", "The reserved principal the operator renders.")
	fs.StringVar(&cfg.PasswordFile, "password-file", "/mosquitto/exporter/password", "The file holding the principal's password.")
	fs.StringVar(&cfg.TLSCert, "tls-cert", "", "The broker's mounted certificate, pinned under TLS. Empty: plain MQTT.")
	fs.StringVar(&cfg.Listen, "listen", ":9234", "The address /metrics is served on.")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	logger := slog.New(slog.NewTextHandler(stderr, nil))

	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		logger.Error("listening", "address", cfg.Listen, "error", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if err := Run(ctx, cfg, listener, logger); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("serving", "error", err)
		return 1
	}
	return 0
}
