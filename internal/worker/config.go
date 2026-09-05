// Package worker is the per-host daemon: one outbound tunnel to the control plane, one
// driver, every PTY and its ring buffer. It shares nothing with the control plane but
// `wire`.
package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"sandboxd/internal/wire"
)

// Config is the worker's whole configuration. Every name here is an existing
// SANDBOXD_* variable that NixOS modules and terraform envs already set — including
// SANDBOXD_WORKER_MAX_SESSIONS, which keeps the old word on purpose (DESIGN.md decision 5).
type Config struct {
	URL          string
	Name         string
	MaxSandboxes int
	DockerSock   string
	Entry        []string
	Driver       string
	Tags         []string
	// Secret is self-generated and never leaves this machine; Fingerprint is what the
	// control plane sees and what approval is bound to.
	Secret      string
	Fingerprint string
	JoinToken   string
	Path        string
}

// hostFile is the on-disk identity, at the same path and in the same shape the TypeScript
// worker wrote, so a host that is migrated keeps its fingerprint and stays approved.
type hostFile struct {
	Secret string `json:"secret"`
	Name   string `json:"name,omitempty"`
}

const (
	defaultURL     = "ws://localhost:8080"
	defaultEntry   = "/usr/local/bin/sandboxd-entry"
	defaultSock    = "/var/run/docker.sock"
	defaultMax     = 4
	secretBytes    = 32
	maxTagLength   = 64
	DriverDocker   = "docker"
	DriverK8s      = "kubernetes" // PLAN.md phase 5
	tagsSeparator  = ","
	configFileMode = 0o600
	configDirMode  = 0o700
)

// LoadConfig reads the environment and the host file, creating the second on first run.
func LoadConfig(getenv func(string) string) (Config, error) {
	path := getenv("SANDBOXD_WORKER_CONFIG")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Config{}, fmt.Errorf("worker: no SANDBOXD_WORKER_CONFIG and no home: %w", err)
		}
		path = filepath.Join(home, ".config", "sandboxd", "host.json")
	}

	file, err := loadHostFile(path)
	if err != nil {
		return Config{}, err
	}

	max := defaultMax
	if raw := getenv("SANDBOXD_WORKER_MAX_SESSIONS"); raw != "" {
		max, err = strconv.Atoi(raw)
		if err != nil || max < 1 {
			return Config{}, fmt.Errorf("worker: SANDBOXD_WORKER_MAX_SESSIONS must be a positive integer, got %q", raw)
		}
	}

	name := getenv("SANDBOXD_WORKER_NAME")
	if name == "" {
		name = file.Name
	}
	if name == "" {
		if name, err = os.Hostname(); err != nil {
			return Config{}, fmt.Errorf("worker: no SANDBOXD_WORKER_NAME and no hostname: %w", err)
		}
	}

	drv := getenv("SANDBOXD_WORKER_DRIVER")
	if drv == "" {
		drv = DriverDocker
	}
	if drv != DriverDocker {
		return Config{}, fmt.Errorf("worker: SANDBOXD_WORKER_DRIVER=%q is not implemented", drv)
	}

	tags, err := loadTags(getenv("SANDBOXD_WORKER_TAGS"), drv)
	if err != nil {
		return Config{}, err
	}

	sum := sha256.Sum256([]byte(file.Secret))
	return Config{
		URL:          strings.TrimRight(orDefault(getenv("SANDBOXD_URL"), defaultURL), "/"),
		Name:         name,
		MaxSandboxes: max,
		DockerSock:   orDefault(getenv("DOCKER_SOCK"), defaultSock),
		Entry:        parseEntry(orDefault(getenv("SANDBOXD_WORKER_ENTRY"), defaultEntry)),
		Driver:       drv,
		Tags:         tags,
		Secret:       file.Secret,
		Fingerprint:  hex.EncodeToString(sum[:]),
		JoinToken:    getenv("SANDBOXD_JOIN_TOKEN"),
		Path:         path,
	}, nil
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

// loadTags reports what this machine is. `arch:`, `os:` and `driver:` are not configurable
// — they are facts — and the operator adds the rest (SPEC.md, "Tags").
func loadTags(extra, drv string) ([]string, error) {
	tags := []string{"arch:" + runtime.GOARCH, "os:" + runtime.GOOS, "driver:" + drv}
	for _, tag := range strings.Split(extra, tagsSeparator) {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if err := validTag(tag); err != nil {
			return nil, fmt.Errorf("worker: SANDBOXD_WORKER_TAGS: %w", err)
		}
		tags = append(tags, tag)
	}
	slices.Sort(tags)
	return slices.Compact(tags), nil
}

// A tag is opaque to the control plane, which compares strings and nothing else. These
// rules exist so a typo fails here rather than as a sandbox that never places.
func validTag(tag string) error {
	if len(tag) > maxTagLength {
		return fmt.Errorf("tag %q is longer than %d characters", tag, maxTagLength)
	}
	if strings.ContainsFunc(tag, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return fmt.Errorf("tag %q contains whitespace or a control character", tag)
	}
	return nil
}

// parseEntry accepts a JSON array (what the NixOS module writes) or a plain command line.
func parseEntry(raw string) []string {
	if strings.HasPrefix(strings.TrimSpace(raw), "[") {
		var out []string
		if err := json.Unmarshal([]byte(raw), &out); err == nil && len(out) > 0 {
			return out
		}
	}
	return strings.Fields(raw)
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
