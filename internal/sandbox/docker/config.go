package docker

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	defaultSock  = "/var/run/docker.sock"
	defaultEntry = "/usr/local/bin/sandboxd-entry"
	defaultMax   = 4
)

// Config is the `docker` block, identical under a worker's `driver:` and the control
// plane's `providers:` — one runtime, one shape, wherever the manager runs. Entry and
// max_sandboxes live here rather than beside the daemon because what a runtime execs and
// how much it can hold are facts about the runtime.
type Config struct {
	Sock string `yaml:"sock"`
	// Entry is the command exec'd in the PTY when a sandbox names none: a command line,
	// or the JSON array the NixOS module writes.
	Entry string `yaml:"entry"`
	Max   int    `yaml:"max_sandboxes"`
}

// Validate fills the defaults and refuses what cannot work.
func (c *Config) Validate() error {
	if c.Sock == "" {
		c.Sock = defaultSock
	}
	if c.Entry == "" {
		c.Entry = defaultEntry
	}
	if c.Max == 0 {
		c.Max = defaultMax
	}
	if c.Max < 0 {
		return fmt.Errorf("max_sandboxes must be positive, got %d", c.Max)
	}
	return nil
}

// MaxSandboxes is what this Engine may hold at once.
func (c Config) MaxSandboxes() int { return c.Max }

// EntryCommand is Entry parsed: a JSON array as written, or a command line split on
// whitespace.
func (c Config) EntryCommand() []string {
	if strings.HasPrefix(strings.TrimSpace(c.Entry), "[") {
		var out []string
		if err := json.Unmarshal([]byte(c.Entry), &out); err == nil && len(out) > 0 {
			return out
		}
	}
	return strings.Fields(c.Entry)
}
