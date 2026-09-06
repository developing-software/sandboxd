// Command sandboxd-api is the control plane: the JSON API, the worker tunnel, the
// browser attach socket and the preview proxy. Wiring only — see internal/cp.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"sandboxd/internal/conf"
	"sandboxd/internal/cp"
	"sandboxd/internal/cp/attach"
	"sandboxd/internal/cp/fleet"
	"sandboxd/internal/cp/hosts"
	"sandboxd/internal/cp/local"
	"sandboxd/internal/cp/preview"
	"sandboxd/internal/cp/server"
	"sandboxd/internal/cp/store"
	"sandboxd/internal/sandbox/docker"
)

const (
	// shutdownGrace is how long in-flight requests have once a signal arrives. Attached
	// terminals and preview sockets are hijacked connections, so Shutdown will not wait
	// for them and this is the whole budget.
	shutdownGrace = 10 * time.Second
	// eventQueue buffers what hosts report while the scheduler is busy placing.
	eventQueue = 64
)

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
	configPath := flag.String("config", "", "path of api.yaml (default: $SANDBOXD_CONFIG, then "+cp.DefaultPath+")")
	check := flag.Bool("check-config", false, "load and validate the configuration, print it with secrets redacted, and exit")
	flag.Parse()

	path, err := conf.Locate(*configPath, os.LookupEnv, cp.DefaultPath)
	if err != nil {
		return err
	}
	cfg, err := cp.Load(path, os.Environ())
	if err != nil {
		return err
	}
	if *check {
		return checkConfig(path, cfg)
	}

	st, err := store.Open(cfg.DB, log)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	tokens := cp.NewTokens(cfg.Auth.Secret)

	// Every provider takes the channel at construction, so there is no cycle to break
	// after the fact: hosts report, the scheduler reacts, and neither knows the other's
	// type. The fleet routes the other direction by host id.
	events := make(chan cp.Event, eventQueue)
	var providers []fleet.Provider
	var tunnel http.HandlerFunc = noTunnel
	if w := cfg.Providers.Workers; w != nil {
		hub := hosts.NewHub(st, w.JoinToken, events, log)
		providers = append(providers, hub)
		tunnel = hub.Serve
	}
	// The daemon's context, not the signal's: a sandbox in this process outlives any one
	// request, and shutdown ends them explicitly below.
	daemon, closeDaemon := context.WithCancel(context.Background())
	defer closeDaemon()
	var embedded *local.Provider
	if d := cfg.Providers.Docker; d != nil {
		drv, err := docker.New(d.Config, local.Fingerprint("docker"), log)
		if err != nil {
			return err
		}
		defer func() { _ = drv.Close() }()
		embedded, err = local.New(daemon, drv, local.Options{
			Name: d.Name, Tags: d.Tags, Driver: "docker", Entry: d.EntryCommand(), Max: d.MaxSandboxes,
		}, st, events, log)
		if err != nil {
			return err
		}
		providers = append(providers, embedded)
	}
	fl := fleet.New(providers...)

	sched := cp.NewScheduler(st, fl, cp.MostFreeSlots, cfg.SandboxEnv, events, log)
	if err := sched.Boot(); err != nil {
		return fmt.Errorf("boot: %w", err)
	}
	go sched.Run(daemon)
	if embedded != nil {
		go embedded.Run(daemon)
	}

	sandboxes := cp.NewSandboxes(cfg, st, fl, sched, tokens)
	handler, err := server.New(server.Deps{
		ServiceToken: cfg.Auth.ServiceToken,
		Tokens:       tokens,
		Sandboxes:    sandboxes,
		Hosts:        hosts.NewService(st, fl, log),
		Attach:       attach.NewBridge(sandboxes, attach.Open(fl.OpenPTY), log),
		Preview:      preview.NewProxy(sandboxes, fl, tokens, cfg.PreviewDomain, log),
		Tunnel:       tunnel,
		Log:          log,
	})
	if err != nil {
		return fmt.Errorf("routes: %w", err)
	}

	srv := &http.Server{
		Addr:    cfg.Listen,
		Handler: handler,
		// No read or write timeout: an attached terminal and a preview WebSocket are both
		// long-lived by design. The header timeout is what keeps a stalled dialler cheap.
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Info("listening",
		"addr", srv.Addr,
		"config", source(path),
		"public", cfg.PublicURL,
		"preview", "*."+cfg.PreviewDomain,
		"db", cfg.DB,
		"docs", cfg.PublicURL+"/doc",
	)
	log.Info("providers",
		"workers", cfg.Providers.Workers != nil,
		"docker", cfg.Providers.Docker != nil,
		"sandbox_env", slices.Sorted(maps.Keys(cfg.SandboxEnv)),
		"enrollment", enrollment(cfg.Providers.Workers),
	)

	errs := make(chan error, 1)
	go func() { errs <- srv.ListenAndServe() }()

	select {
	case err := <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	grace, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if embedded != nil {
		embedded.Close(grace)
	}
	if err := srv.Shutdown(grace); err != nil {
		return srv.Close()
	}
	return nil
}

// checkConfig is `--check-config`: the source used, what the file made the environment
// irrelevant to, and the effective configuration with secrets redacted.
func checkConfig(path string, cfg cp.Config) error {
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

// noTunnel serves GET /tunnel when the workers provider is not configured.
func noTunnel(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "the workers provider is not enabled on this control plane", http.StatusNotFound)
}

func enrollment(w *cp.Workers) string {
	switch {
	case w == nil:
		return "none"
	case w.JoinToken != "":
		return "join token or code"
	}
	return "code"
}
