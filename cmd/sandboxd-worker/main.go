// Command sandboxd-worker is the per-host daemon: one outbound tunnel to the control
// plane, one driver, every PTY. Wiring only — see internal/worker.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"sandboxd/internal/conf"
	"sandboxd/internal/sandbox"
	"sandboxd/internal/wire"
	"sandboxd/internal/worker"
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
	configPath := flag.String("config", "", "path of worker.yaml (default: $SANDBOXD_CONFIG, then "+worker.DefaultPath+")")
	check := flag.Bool("check-config", false, "load and validate the configuration, print it with secrets redacted, and exit")
	flag.Parse()

	path, err := conf.Locate(*configPath, os.LookupEnv, worker.DefaultPath)
	if err != nil {
		return err
	}
	cfg, err := worker.Load(path, os.LookupEnv)
	if err != nil {
		return err
	}
	if *check {
		return checkConfig(path, cfg)
	}
	if path == "" && os.Getenv("SANDBOXD_WORKER_CONFIG") != "" {
		log.Warn("SANDBOXD_WORKER_CONFIG is now SANDBOXD_WORKER_IDENTITY; the old name still works this release")
	}
	if err := cfg.LoadIdentity(); err != nil {
		return err
	}

	drv, err := cfg.Driver.Open(cfg.Fingerprint, log)
	if err != nil {
		return err
	}
	defer func() { _ = drv.Close() }()

	// The daemon outlives the signal: sandboxes are ended and reported before the tunnel
	// is allowed to go, which is why this context is not the one the signal cancels.
	daemon, closeDaemon := context.WithCancel(context.Background())
	defer closeDaemon()

	events := make(chan sandbox.Event, 64)
	mgr := sandbox.NewManager(daemon, drv, cfg.Driver.EntryCommand(), cfg.Driver.MaxSandboxes(), events, log)
	tunnel := worker.NewTunnel(cfg, mgr, events, log)

	// A sandbox from a previous run cannot be re-attached: its PTY and ring died with
	// that process, so the container goes.
	if err := mgr.Sweep(ctx); err != nil {
		return err
	}

	log.Info("starting",
		"config", source(path),
		"name", cfg.Name,
		"cp", cfg.URL,
		"driver", cfg.Driver.Name(),
		"max_sandboxes", cfg.Driver.MaxSandboxes(),
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

// checkConfig is `--check-config`: the source used, what the file made the environment
// irrelevant to, and the effective configuration with secrets redacted.
func checkConfig(path string, cfg worker.Config) error {
	fmt.Println("# source:", source(path))
	if ignored := conf.Ignored(os.Environ()); path != "" && len(ignored) > 0 {
		fmt.Println("# ignored (a config file is in use; only ${VAR} reads the environment):", ignored)
	}
	return conf.Print(os.Stdout, cfg.Redacted())
}

func source(path string) string {
	if path == "" {
		return "environment (no config file found)"
	}
	return path
}
