package cp

import (
	"fmt"
	"strconv"
	"strings"
)

// Control-plane configuration. The API is generic: it knows images, commands, env and
// tags, never presets — those belong to the client.

const (
	sandboxEnvPrefix = "SANDBOXD_SANDBOX_ENV_"
	devServiceToken  = "dev-token"
)

type Config struct {
	Port int
	// ServiceToken is the parent app's bearer credential for every /hosts and /sandboxes route.
	ServiceToken string
	// Secret signs the capability tokens. Defaults to ServiceToken.
	Secret string
	// PublicURL is what browsers are told to connect to, e.g. https://api.example.com.
	PublicURL string
	// PreviewDomain is the wildcard the preview hosts live under, e.g. preview.example.com.
	PreviewDomain string
	// SandboxEnv is operator-level env merged under every sandbox at placement time and
	// never persisted, from SANDBOXD_SANDBOX_ENV_<NAME> plus the LLM_* shorthands.
	SandboxEnv map[string]string
	// JoinToken, when set, approves a hello that carries it without the printed code.
	JoinToken string
	DBPath    string
}

// LoadConfig reads the environment. It takes the whole environment rather than a lookup
// function — unlike the worker's — because SANDBOXD_SANDBOX_ENV_* is a prefix scan.
func LoadConfig(environ []string) (Config, error) {
	env := map[string]string{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}

	cfg := Config{
		ServiceToken:  env["SANDBOXD_SERVICE_TOKEN"],
		PreviewDomain: or(env["SANDBOXD_PREVIEW_DOMAIN"], "preview.localhost"),
		JoinToken:     env["SANDBOXD_JOIN_TOKEN"],
		DBPath:        or(env["SANDBOXD_DB"], "cp.db"),
		SandboxEnv:    map[string]string{},
	}

	// Fails closed, unlike the TypeScript control plane it replaces (PLAN.md, "Bugs to
	// fix in the port, not carry"): a production binary that silently accepts a published
	// default token is worse than one that refuses to boot.
	if cfg.ServiceToken == "" {
		if env["SANDBOXD_DEV"] != "1" {
			return Config{}, fmt.Errorf(
				"SANDBOXD_SERVICE_TOKEN is not set; set it, or SANDBOXD_DEV=1 to accept %q locally",
				devServiceToken)
		}
		cfg.ServiceToken = devServiceToken
	}
	cfg.Secret = or(env["SANDBOXD_SECRET"], cfg.ServiceToken)

	port := 8080
	if s := env["SANDBOXD_PORT"]; s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 65535 {
			return Config{}, fmt.Errorf("SANDBOXD_PORT: %q is not a port", s)
		}
		port = n
	}
	cfg.Port = port
	cfg.PublicURL = strings.TrimRight(
		or(env["SANDBOXD_PUBLIC_URL"], fmt.Sprintf("http://localhost:%d", port)), "/")

	// Plain LLM_* are accepted too, since that is what people naturally export.
	if base := or(env["SANDBOXD_LLM_BASE_URL"], env["LLM_BASE_URL"]); base != "" {
		cfg.SandboxEnv["LLM_BASE_URL"] = strings.TrimRight(base, "/")
	}
	if key := or(env["SANDBOXD_LLM_API_KEY"], env["LLM_API_KEY"]); key != "" {
		cfg.SandboxEnv["LLM_API_KEY"] = key
	}
	for k, v := range env {
		if name, ok := strings.CutPrefix(k, sandboxEnvPrefix); ok && name != "" {
			cfg.SandboxEnv[name] = v
		}
	}
	return cfg, nil
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
