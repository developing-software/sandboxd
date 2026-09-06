package cp

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"

	"sandboxd/internal/conf"
	"sandboxd/internal/sandbox"
	"sandboxd/internal/sandbox/docker"
)

// Control-plane configuration. The API is generic: it knows images, commands, env and
// tags, never presets — those belong to the client.

// DefaultPath is where the file is looked for when neither --config nor SANDBOXD_CONFIG
// names one.
const DefaultPath = "/etc/sandboxd/api.yaml"

const (
	sandboxEnvPrefix = "SANDBOXD_SANDBOX_ENV_"
	devServiceToken  = "dev-token"
	defaultListen    = ":8080"
)

// Config is api.yaml. Providers are blocks keyed by type: presence enables.
type Config struct {
	Listen string `yaml:"listen"`
	// PublicURL is what browsers are told to connect to, e.g. https://api.example.com.
	PublicURL string `yaml:"public_url"`
	// PreviewDomain is the wildcard the preview hosts live under, e.g. preview.example.com.
	PreviewDomain string `yaml:"preview_domain"`
	DB            string `yaml:"db"`
	Auth          Auth   `yaml:"auth"`
	// SandboxEnv is operator-level env merged under every sandbox at placement time and
	// never persisted. SandboxEnvFiles adds values read from files, for the keys.
	SandboxEnv      map[string]string `yaml:"sandbox_env,omitempty"`
	SandboxEnvFiles map[string]string `yaml:"sandbox_env_files,omitempty"`
	Providers       Providers         `yaml:"providers"`
}

type Auth struct {
	// ServiceToken is the parent app's bearer credential for every /hosts and /sandboxes route.
	ServiceToken     string `yaml:"service_token,omitempty"`
	ServiceTokenFile string `yaml:"service_token_file,omitempty"`
	// Secret signs the capability tokens. Defaults to ServiceToken.
	Secret     string `yaml:"secret,omitempty"`
	SecretFile string `yaml:"secret_file,omitempty"`
}

// Providers is where sandboxes may run. Absent, or empty, means `workers: {}`; anything
// present means exactly what is present, so a file that names only `docker` runs no
// tunnel at all.
type Providers struct {
	Workers *Workers        `yaml:"workers,omitempty"`
	Docker  *DockerProvider `yaml:"docker,omitempty"`
}

// Workers is the tunnel: machines running sandboxd-worker that dial in.
type Workers struct {
	// JoinToken, when set, approves a hello that carries it without the printed code.
	JoinToken     string `yaml:"join_token,omitempty"`
	JoinTokenFile string `yaml:"join_token_file,omitempty"`
}

// DockerProvider is a manager in this process over the Docker driver, shown as one host.
// Name and Tags are the provider's own; the rest is the driver's block, the same one a
// worker carries under `driver.docker`.
type DockerProvider struct {
	Name          string   `yaml:"name"`
	Tags          []string `yaml:"tags"`
	docker.Config `yaml:",inline"`
}

// Load reads the file at path, or the legacy environment when path is empty, and
// validates either. It takes the whole environment rather than a lookup because the
// legacy SANDBOXD_SANDBOX_ENV_* is a prefix scan.
func Load(path string, environ []string) (Config, error) {
	env := map[string]string{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	var cfg Config
	var err error
	if path == "" {
		cfg, err = legacy(env)
	} else {
		cfg, err = conf.Load[Config](path, func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	}
	if err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// legacy is today's SANDBOXD_* variables, applied unchanged when no file is found. It
// keeps deployed units and scripts/dev booting; it is compatibility, not design.
func legacy(env map[string]string) (Config, error) {
	cfg := Config{
		Listen:        ":" + or(env["SANDBOXD_PORT"], "8080"),
		PublicURL:     env["SANDBOXD_PUBLIC_URL"],
		PreviewDomain: env["SANDBOXD_PREVIEW_DOMAIN"],
		DB:            env["SANDBOXD_DB"],
		Auth:          Auth{ServiceToken: env["SANDBOXD_SERVICE_TOKEN"], Secret: env["SANDBOXD_SECRET"]},
		SandboxEnv:    map[string]string{},
		Providers:     Providers{Workers: &Workers{JoinToken: env["SANDBOXD_JOIN_TOKEN"]}},
	}
	// Fails closed: a production binary that silently accepts a published default token
	// is worse than one that refuses to boot.
	if cfg.Auth.ServiceToken == "" {
		if env["SANDBOXD_DEV"] != "1" {
			return Config{}, fmt.Errorf(
				"SANDBOXD_SERVICE_TOKEN is not set; set it, or SANDBOXD_DEV=1 to accept %q locally",
				devServiceToken)
		}
		cfg.Auth.ServiceToken = devServiceToken
	}
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

// Validate fills defaults, resolves every `_file` twin and refuses what cannot boot.
func (c *Config) Validate() error {
	if c.Listen == "" {
		c.Listen = defaultListen
	}
	_, port, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("listen: %q is not a port", port)
	}
	c.PublicURL = strings.TrimRight(or(c.PublicURL, "http://localhost:"+port), "/")
	c.PreviewDomain = or(c.PreviewDomain, "preview.localhost")
	c.DB = or(c.DB, "cp.db")

	if c.Auth.ServiceToken, err = conf.Secret("auth.service_token", c.Auth.ServiceToken, c.Auth.ServiceTokenFile); err != nil {
		return err
	}
	if c.Auth.ServiceToken == "" {
		return errors.New("auth.service_token is required")
	}
	if c.Auth.Secret, err = conf.Secret("auth.secret", c.Auth.Secret, c.Auth.SecretFile); err != nil {
		return err
	}
	c.Auth.Secret = or(c.Auth.Secret, c.Auth.ServiceToken)
	c.Auth.ServiceTokenFile, c.Auth.SecretFile = "", ""

	if c.SandboxEnv == nil {
		c.SandboxEnv = map[string]string{}
	}
	for _, k := range slices.Sorted(maps.Keys(c.SandboxEnvFiles)) {
		if _, twice := c.SandboxEnv[k]; twice {
			return fmt.Errorf("sandbox_env.%s and sandbox_env_files.%s are both set", k, k)
		}
		b, err := os.ReadFile(c.SandboxEnvFiles[k])
		if err != nil {
			return fmt.Errorf("sandbox_env_files.%s: %w", k, err)
		}
		c.SandboxEnv[k] = strings.TrimSpace(string(b))
	}
	c.SandboxEnvFiles = nil
	if err := ValidateEnv("sandbox_env", c.SandboxEnv); err != nil {
		return err
	}
	return c.Providers.validate()
}

func (p *Providers) validate() error {
	if p.Workers == nil && p.Docker == nil {
		p.Workers = &Workers{}
	}
	if w := p.Workers; w != nil {
		token, err := conf.Secret("providers.workers.join_token", w.JoinToken, w.JoinTokenFile)
		if err != nil {
			return err
		}
		w.JoinToken, w.JoinTokenFile = token, ""
	}
	if d := p.Docker; d != nil {
		if err := d.Validate(); err != nil {
			return fmt.Errorf("providers.docker: %w", err)
		}
		if d.Name == "" {
			if d.Name, _ = os.Hostname(); d.Name == "" {
				d.Name = "docker"
			}
		}
		tags, err := sandbox.Tags("docker", d.Tags)
		if err != nil {
			return fmt.Errorf("providers.docker.tags: %w", err)
		}
		d.Tags = tags
	}
	return nil
}

// Redacted is what --check-config prints: every secret replaced, and every sandbox_env
// value with it, since the loader cannot know which of those a `${VAR}` made secret.
func (c Config) Redacted() Config {
	c.Auth.ServiceToken = mask(c.Auth.ServiceToken)
	c.Auth.Secret = mask(c.Auth.Secret)
	if c.Providers.Workers != nil {
		w := *c.Providers.Workers
		w.JoinToken = mask(w.JoinToken)
		c.Providers.Workers = &w
	}
	env := make(map[string]string, len(c.SandboxEnv))
	for k, v := range c.SandboxEnv {
		env[k] = mask(v)
	}
	c.SandboxEnv = env
	return c
}

func mask(s string) string {
	if s == "" {
		return ""
	}
	return "***"
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
