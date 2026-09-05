package cp

import (
	"maps"
	"testing"
)

func TestConfigFailsClosedOnTheServiceToken(t *testing.T) {
	// The one intended break from "every variable and default unchanged": the TypeScript
	// control plane defaulted to a published token with a warning (PLAN.md, "Bugs to fix
	// in the port, not carry").
	if _, err := LoadConfig(nil); err == nil {
		t.Fatal("a control plane with no service token must refuse to boot")
	}
	cfg, err := LoadConfig([]string{"SANDBOXD_DEV=1"})
	if err != nil {
		t.Fatalf("dev mode still has to boot: %v", err)
	}
	if cfg.ServiceToken != devServiceToken {
		t.Errorf("dev token = %q", cfg.ServiceToken)
	}
}

func TestConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig([]string{"SANDBOXD_SERVICE_TOKEN=t"})
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Port:          8080,
		ServiceToken:  "t",
		Secret:        "t", // the service token signs the capability tokens unless told otherwise
		PublicURL:     "http://localhost:8080",
		PreviewDomain: "preview.localhost",
		DBPath:        "cp.db",
		SandboxEnv:    map[string]string{},
	}
	if !equal(cfg, want) {
		t.Errorf("defaults = %+v, want %+v", cfg, want)
	}
}

func TestConfigReadsTheEnvironment(t *testing.T) {
	cfg, err := LoadConfig([]string{
		"SANDBOXD_SERVICE_TOKEN=t",
		"SANDBOXD_SECRET=signing",
		"SANDBOXD_PORT=9000",
		"SANDBOXD_PUBLIC_URL=https://cp.example.com/",
		"SANDBOXD_PREVIEW_DOMAIN=preview.example.com",
		"SANDBOXD_JOIN_TOKEN=jt",
		"SANDBOXD_DB=/var/lib/sandboxd/cp.db",
		// Operator env: the prefix, and the two shorthands people actually export.
		"SANDBOXD_SANDBOX_ENV_HTTP_PROXY=http://proxy",
		"LLM_BASE_URL=http://llm/",
		"SANDBOXD_LLM_API_KEY=k",
		"UNRELATED=x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 9000 || cfg.Secret != "signing" || cfg.JoinToken != "jt" {
		t.Errorf("config = %+v", cfg)
	}
	// The trailing slash goes, so joining paths onto it does not double up.
	if cfg.PublicURL != "https://cp.example.com" {
		t.Errorf("public url = %q", cfg.PublicURL)
	}
	want := map[string]string{
		"HTTP_PROXY":   "http://proxy",
		"LLM_BASE_URL": "http://llm",
		"LLM_API_KEY":  "k",
	}
	if !maps.Equal(cfg.SandboxEnv, want) {
		t.Errorf("sandbox env = %v, want %v", cfg.SandboxEnv, want)
	}
}

func TestConfigRejectsANonPort(t *testing.T) {
	_, err := LoadConfig([]string{"SANDBOXD_SERVICE_TOKEN=t", "SANDBOXD_PORT=nope"})
	if err == nil {
		t.Fatal("a port that is not a number must not silently become 8080")
	}
	if _, err := LoadConfig([]string{"SANDBOXD_SERVICE_TOKEN=t", "SANDBOXD_PORT=70000"}); err == nil {
		t.Error("70000 is not a port")
	}
}

func equal(a, b Config) bool {
	return a.Port == b.Port && a.ServiceToken == b.ServiceToken && a.Secret == b.Secret &&
		a.PublicURL == b.PublicURL && a.PreviewDomain == b.PreviewDomain &&
		a.JoinToken == b.JoinToken && a.DBPath == b.DBPath &&
		maps.Equal(a.SandboxEnv, b.SandboxEnv)
}
