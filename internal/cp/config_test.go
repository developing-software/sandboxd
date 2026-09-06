package cp

import (
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"sandboxd/internal/conf"
	"sandboxd/internal/sandbox/docker"
)

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFileConfig(t *testing.T) {
	token := writeFile(t, "token", "t\n")
	key := writeFile(t, "key", "k\n")
	path := writeFile(t, "api.yaml", `
listen: ":9000"
public_url: https://cp.example.com/
auth:
  service_token_file: `+token+`
  secret: ${SIGNING:-signing}
sandbox_env:
  LLM_BASE_URL: http://llm
sandbox_env_files:
  LLM_API_KEY: `+key+`
providers:
  workers:
    join_token: jt
  docker:
    name: cp
    tags: [gpu]
    max_sandboxes: 2
`)
	cfg, err := Load(path, conf.NewEnv([]string{"UNRELATED=x", "SANDBOXD_PORT=1"}))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Listen:        ":9000",
		PublicURL:     "https://cp.example.com", // the trailing slash goes
		PreviewDomain: "preview.localhost",
		DB:            "cp.db",
		Auth:          Auth{ServiceToken: "t", Secret: "signing"},
		SandboxEnv:    map[string]string{"LLM_BASE_URL": "http://llm", "LLM_API_KEY": "k"},
		Providers: Providers{
			Workers: &Workers{JoinToken: "jt"},
			Docker: &DockerProvider{
				Name:   "cp",
				Tags:   []string{"arch:" + runtime.GOARCH, "driver:docker", "gpu", "os:" + runtime.GOOS},
				Config: docker.Config{Sock: "/var/run/docker.sock", Entry: "/usr/local/bin/sandboxd-entry", Max: 2},
			},
		},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("config\n got %+v %+v %+v\nwant %+v %+v %+v",
			cfg, cfg.Providers.Workers, cfg.Providers.Docker, want, want.Providers.Workers, want.Providers.Docker)
	}

	// The redacted print never carries a secret, whichever key it arrived under.
	r := cfg.Redacted()
	if r.Auth.ServiceToken != "***" || r.Auth.Secret != "***" || r.Providers.Workers.JoinToken != "***" {
		t.Errorf("redacted = %+v %+v", r.Auth, r.Providers.Workers)
	}
	for k, v := range r.SandboxEnv {
		if v != "***" {
			t.Errorf("sandbox_env.%s = %q in the redacted print", k, v)
		}
	}
	if cfg.Auth.ServiceToken != "t" {
		t.Error("Redacted must copy, not mutate")
	}
}

func TestFileConfigProviders(t *testing.T) {
	load := func(t *testing.T, providers string) Config {
		t.Helper()
		cfg, err := Load(writeFile(t, "api.yaml", "auth: {service_token: t}\n"+providers), conf.NewEnv(nil))
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	// Absent means the tunnel and nothing else.
	if p := load(t, "").Providers; p.Workers == nil || p.Docker != nil {
		t.Errorf("absent providers = %+v, want workers only", p)
	}
	// Present means exactly what is present: docker alone runs no tunnel.
	if p := load(t, "providers: {docker: {}}\n").Providers; p.Workers != nil || p.Docker == nil {
		t.Errorf("docker-only providers = %+v", p)
	}
	if d := load(t, "providers: {docker: {}}\n").Providers.Docker; d.Name == "" || d.Max != 4 {
		t.Errorf("docker defaults = %+v", d)
	}
}

func TestFileConfigRejects(t *testing.T) {
	for name, body := range map[string]string{
		"no service token":         "listen: ':1'\n",
		"an unknown key":           "auth: {service_token: t}\nport: 1\n",
		"both token twins":         "auth: {service_token: t, service_token_file: /x}\n",
		"a bad listen":             "auth: {service_token: t}\nlisten: '9000'\n",
		"a reserved env":           "auth: {service_token: t}\nsandbox_env: {TERM: x}\n",
		"a provider not yet built": "auth: {service_token: t}\nproviders: {kubernetes: {}}\n",
		"a bad provider tag":       "auth: {service_token: t}\nproviders: {docker: {tags: ['a b']}}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeFile(t, "api.yaml", body), conf.NewEnv(nil)); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestLegacyEnvFailsClosedOnTheServiceToken(t *testing.T) {
	// There is no default token: the published dev value is accepted only when asked for.
	if _, err := Load("", conf.NewEnv(nil)); err == nil {
		t.Fatal("a control plane with no service token must refuse to boot")
	}
	cfg, err := Load("", conf.NewEnv([]string{"SANDBOXD_DEV=1"}))
	if err != nil {
		t.Fatalf("dev mode still has to boot: %v", err)
	}
	if cfg.Auth.ServiceToken != devServiceToken {
		t.Errorf("dev token = %q", cfg.Auth.ServiceToken)
	}
}

func TestLegacyEnvDefaults(t *testing.T) {
	cfg, err := Load("", conf.NewEnv([]string{"SANDBOXD_SERVICE_TOKEN=t"}))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Listen:        ":8080",
		PublicURL:     "http://localhost:8080",
		PreviewDomain: "preview.localhost",
		DB:            "cp.db",
		Auth:          Auth{ServiceToken: "t", Secret: "t"}, // the service token signs unless told otherwise
		SandboxEnv:    map[string]string{},
		Providers:     Providers{Workers: &Workers{}},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("defaults = %+v, want %+v", cfg, want)
	}
}

func TestLegacyEnvIsReadUnchanged(t *testing.T) {
	cfg, err := Load("", conf.NewEnv([]string{
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
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":9000" || cfg.Auth.Secret != "signing" || cfg.Providers.Workers.JoinToken != "jt" {
		t.Errorf("config = %+v", cfg)
	}
	if cfg.PublicURL != "https://cp.example.com" || cfg.DB != "/var/lib/sandboxd/cp.db" {
		t.Errorf("config = %+v", cfg)
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

func TestLegacyEnvRejectsANonPort(t *testing.T) {
	for _, port := range []string{"nope", "70000"} {
		_, err := Load("", conf.NewEnv([]string{"SANDBOXD_SERVICE_TOKEN=t", "SANDBOXD_PORT=" + port}))
		if err == nil || !strings.Contains(err.Error(), "port") {
			t.Errorf("SANDBOXD_PORT=%s: err = %v, want a port error rather than a silent 8080", port, err)
		}
	}
}
