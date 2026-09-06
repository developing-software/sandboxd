// Command sandboxd-api is the control plane: the JSON API, the worker tunnel, the
// browser attach socket and the preview proxy. Wiring only — see internal/cp.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"sandboxd/internal/cp"
	"sandboxd/internal/cp/attach"
	"sandboxd/internal/cp/hosts"
	cphttp "sandboxd/internal/cp/http"
	"sandboxd/internal/cp/preview"
	"sandboxd/internal/cp/store"
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
	cfg, err := cp.LoadConfig(os.Environ())
	if err != nil {
		return err
	}

	st, err := store.Open(cfg.DBPath, log)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	tokens := cp.NewTokens(cfg.Secret)

	// The hub takes the channel at construction, so there is no cycle to break after the
	// fact: hosts report, the scheduler reacts, and neither knows the other's type.
	events := make(chan cp.Event, eventQueue)
	hub := hosts.NewHub(st, cfg.JoinToken, events, log)
	sched := cp.NewScheduler(st, hub, cfg.SandboxEnv, events, log)
	if err := sched.Boot(); err != nil {
		return fmt.Errorf("boot: %w", err)
	}
	go sched.Run(ctx)

	sandboxes := cp.NewSandboxes(cfg, st, hub, sched, tokens)
	handler, err := cphttp.New(cphttp.Deps{
		ServiceToken: cfg.ServiceToken,
		Tokens:       tokens,
		Sandboxes:    sandboxes,
		Hosts:        hosts.NewService(st, hub, log),
		Attach:       attach.NewBridge(sandboxes, hub, log),
		Preview:      preview.NewProxy(sandboxes, hub, tokens, cfg.PreviewDomain, log),
		Tunnel:       hub.Serve,
		Log:          log,
	})
	if err != nil {
		return fmt.Errorf("routes: %w", err)
	}

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: handler,
		// No read or write timeout: an attached terminal and a preview WebSocket are both
		// long-lived by design. The header timeout is what keeps a stalled dialler cheap.
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Info("listening",
		"addr", srv.Addr,
		"public", cfg.PublicURL,
		"preview", "*."+cfg.PreviewDomain,
		"db", cfg.DBPath,
		"docs", cfg.PublicURL+"/doc",
	)
	log.Info("defaults",
		"sandbox_env", slices.Sorted(maps.Keys(cfg.SandboxEnv)),
		"enrollment", enrollment(cfg.JoinToken),
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
	if err := srv.Shutdown(grace); err != nil {
		return srv.Close()
	}
	return nil
}

func enrollment(joinToken string) string {
	if joinToken != "" {
		return "join token or code"
	}
	return "code"
}
