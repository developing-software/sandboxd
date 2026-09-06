// Command sandboxd-worker is the per-host daemon: one outbound tunnel to the control
// plane, one driver, every PTY. Wiring only — see internal/worker.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"sandboxd/internal/wire"
	"sandboxd/internal/worker"
	"sandboxd/internal/worker/driver"
)

// How long shutdown waits for every sandbox to be reported lost before the socket goes.
const shutdownGrace = 10 * time.Second

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, log); err != nil {
		log.Error("exit", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger) error {
	cfg, err := worker.LoadConfig(os.Getenv)
	if err != nil {
		return err
	}

	drv, err := driver.NewDocker(cfg.DockerSock, cfg.Fingerprint, log)
	if err != nil {
		return err
	}
	defer func() { _ = drv.Close() }()

	// The daemon outlives the signal: sandboxes are ended and reported before the tunnel
	// is allowed to go, which is why this context is not the one the signal cancels.
	daemon, closeDaemon := context.WithCancel(context.Background())
	defer closeDaemon()

	events := make(chan worker.Event, 64)
	mgr := worker.NewManager(daemon, drv, cfg.Entry, events, log)
	tunnel := worker.NewTunnel(cfg, mgr, drv, events, log)

	// A sandbox from a previous run cannot be re-attached: its PTY and ring died with
	// that process, so the container goes.
	if err := mgr.Sweep(ctx); err != nil {
		return err
	}

	log.Info("starting",
		"name", cfg.Name,
		"cp", cfg.URL,
		"max_sandboxes", cfg.MaxSandboxes,
		"tags", cfg.Tags,
		"fingerprint", cfg.Fingerprint[:12],
	)

	go mgr.Run(daemon)
	go tunnel.Run(daemon)

	<-ctx.Done()
	log.Info("shutting down")

	grace, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	mgr.EndAll(grace, wire.EndLost)
	tunnel.Flush(grace)
	return nil
}
