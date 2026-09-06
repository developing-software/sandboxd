// Package worker is the per-host daemon: one outbound tunnel to the control plane, one
// driver, every PTY and its ring buffer. It shares nothing with the control plane but
// `wire` and `sandbox`.
package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"sandboxd/internal/conf"
	"sandboxd/internal/sandbox"
	"sandboxd/internal/sandbox/docker"
	"sandboxd/internal/wire"
)

// DefaultPath is where the file is looked for when neither --config nor SANDBOXD_CONFIG
// names one.
const DefaultPath = "/etc/sandboxd/worker.yaml"

// Config is the worker's file. On a worker the daemon is the provider, so `name` and
// `tags` sit at the top and the runtime under `driver:`, one block, keyed by its name.
type Config struct {
	URL           string   `yaml:"url"`
	Name          string   `yaml:"name"`
	Tags          []string `yaml:"tags"`
	JoinToken     string   `yaml:"join_token,omitempty"`
	JoinTokenFile string   `yaml:"join_token_file,omitempty"`
	// Identity is the path of the host's self-generated secret: state, not config, which
	// is why it stays a separate file.
	Identity string `yaml:"identity"`
	Driver   Driver `yaml:"driver"`

	// Secret never leaves this machine; Fingerprint is what the control plane sees and
	// what approval is bound to. Both come from LoadIdentity, never from the file.
	Secret      string `yaml:"-"`
	Fingerprint string `yaml:"-"`
}

// Driver is the `driver:` block: exactly one of these is set, and its keys are the same
// block the control plane's `providers.<name>` carries.
type Driver struct {
	Docker *docker.Config `yaml:"docker,omitempty"`
}

// Name is the driver's tag value, and the block that was chosen.
func (d Driver) Name() string {
	if d.Docker != nil {
		return "docker"
	}
	return ""
}

// hostFile is the on-disk identity. Its path and shape are fixed: a host provisioned by an
// earlier release keeps its fingerprint across upgrades and stays approved.
type hostFile struct {
	Secret string `json:"secret"`
	Name   string `json:"name,omitempty"`
}

const (
	defaultURL     = "ws://localhost:8080"
	secretBytes    = 32
	configFileMode = 0o600
	configDirMode  = 0o700
)

// Load reads the file at path, or the legacy environment when path is empty, and
// validates either. The identity is loaded separately: reading config must not create
// state on disk.
func Load(path string, lookup conf.Lookup) (Config, error) {
	var cfg Config
	var err error
	if path == "" {
		cfg, err = legacy(lookup)
	} else {
		cfg, err = conf.Load[Config](path, lookup)
	}
	if err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("worker: %w", err)
	}
	return cfg, nil
}

// legacy is today's SANDBOXD_* variables, applied unchanged when no file is found. It
// keeps deployed units and scripts/dev booting; it is compatibility, not design.
func legacy(lookup conf.Lookup) (Config, error) {
	get := func(k string) string { v, _ := lookup(k); return v }
	drv := docker.Config{Sock: get("DOCKER_SOCK"), Entry: get("SANDBOXD_WORKER_ENTRY")}
	if raw := get("SANDBOXD_WORKER_MAX_SESSIONS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("worker: SANDBOXD_WORKER_MAX_SESSIONS must be a positive integer, got %q", raw)
		}
		drv.MaxSandboxes = n
	}
	if name := get("SANDBOXD_WORKER_DRIVER"); name != "" && name != "docker" {
		return Config{}, fmt.Errorf("worker: SANDBOXD_WORKER_DRIVER=%q is not implemented", name)
	}
	identity := get("SANDBOXD_WORKER_IDENTITY")
	if identity == "" {
		identity = get("SANDBOXD_WORKER_CONFIG") // the old name; cmd warns about it
	}
	var tags []string
	if raw := get("SANDBOXD_WORKER_TAGS"); raw != "" {
		tags = strings.Split(raw, ",")
	}
	return Config{
		URL:       get("SANDBOXD_URL"),
		Name:      get("SANDBOXD_WORKER_NAME"),
		Tags:      tags,
		JoinToken: get("SANDBOXD_JOIN_TOKEN"),
		Identity:  identity,
		Driver:    Driver{Docker: &drv},
	}, nil
}

// Validate fills defaults, resolves the secret twin and adds the fact tags. The driver
// block validates itself; this only insists there is exactly one.
func (c *Config) Validate() error {
	c.URL = strings.TrimRight(c.URL, "/")
	if c.URL == "" {
		c.URL = defaultURL
	}
	if c.Identity == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("no identity path and no home: %w", err)
		}
		c.Identity = filepath.Join(home, ".config", "sandboxd", "host.json")
	}
	token, err := conf.Secret("join_token", c.JoinToken, c.JoinTokenFile)
	if err != nil {
		return err
	}
	c.JoinToken, c.JoinTokenFile = token, ""

	if c.Driver.Docker == nil {
		return errors.New("driver: one block is required (docker)")
	}
	if err := c.Driver.Docker.Validate(); err != nil {
		return fmt.Errorf("driver.docker: %w", err)
	}
	if c.Tags, err = sandbox.Tags(c.Driver.Name(), c.Tags); err != nil {
		return fmt.Errorf("tags: %w", err)
	}
	return nil
}

// LoadIdentity reads the host file at c.Identity, creating it on first run, and fills
// Secret, Fingerprint and — when nothing else named the host — Name.
func (c *Config) LoadIdentity() error {
	file, err := loadHostFile(c.Identity)
	if err != nil {
		return err
	}
	if c.Name == "" {
		c.Name = file.Name
	}
	if c.Name == "" {
		if c.Name, err = os.Hostname(); err != nil {
			return fmt.Errorf("worker: no name and no hostname: %w", err)
		}
	}
	sum := sha256.Sum256([]byte(file.Secret))
	c.Secret = file.Secret
	c.Fingerprint = hex.EncodeToString(sum[:])
	return nil
}

// Redacted is what --check-config prints: the effective configuration with every secret
// replaced, so the output can be pasted into a bug report.
func (c Config) Redacted() Config {
	if c.JoinToken != "" {
		c.JoinToken = "***"
	}
	return c
}

func loadHostFile(path string) (hostFile, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		var file hostFile
		if err := json.Unmarshal(raw, &file); err != nil {
			return hostFile{}, fmt.Errorf("worker: %s is not valid JSON: %w", path, err)
		}
		if file.Secret == "" {
			return hostFile{}, fmt.Errorf("worker: %s has no secret", path)
		}
		return file, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return hostFile{}, fmt.Errorf("worker: read %s: %w", path, err)
	}

	file := hostFile{Secret: wire.RandomID(secretBytes)}
	body, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return hostFile{}, fmt.Errorf("worker: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), configDirMode); err != nil {
		return hostFile{}, fmt.Errorf("worker: create %s: %w", filepath.Dir(path), err)
	}
	// The secret is this host's identity: nobody else on the machine reads it.
	if err := os.WriteFile(path, body, configFileMode); err != nil {
		return hostFile{}, fmt.Errorf("worker: write %s: %w", path, err)
	}
	return file, nil
}
