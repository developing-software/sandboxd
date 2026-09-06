// Package drivers is the one place a driver's name meets its package. The block under a
// worker's `driver:` and under a control plane's `providers.<name>` is decoded here,
// validated here and opened here, so neither cmd names Docker. A new driver is one field
// in Config, one case in each switch below, and its own package under internal/sandbox.
package drivers

import (
	"errors"
	"fmt"
	"io"
	"log/slog"

	"sandboxd/internal/sandbox"
	"sandboxd/internal/sandbox/docker"
)

// Driver is what Open hands out: a sandbox.Driver that holds a client and must be closed.
type Driver interface {
	sandbox.Driver
	io.Closer
}

// block is what every driver's config provides. Entry and MaxSandboxes are in the block
// rather than beside the daemon because what a runtime execs and how much it can hold
// are facts about the runtime.
type block interface {
	Validate() error
	EntryCommand() []string
	MaxSandboxes() int
}

// Config is the block, keyed by driver name. Exactly one is set.
type Config struct {
	Docker *docker.Config `yaml:"docker,omitempty"`
}

// block is the chosen name and its config, or "" and nil.
func (c Config) block() (string, block) {
	switch {
	case c.Docker != nil:
		return "docker", c.Docker
	}
	return "", nil
}

// Validate insists on exactly one block and validates it.
func (c *Config) Validate() error {
	name, b := c.block()
	if b == nil {
		return errors.New("one driver block is required (docker)")
	}
	if err := b.Validate(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// Name is the driver's tag value, and which block was chosen.
func (c Config) Name() string {
	name, _ := c.block()
	return name
}

// Open connects the chosen driver. owner scopes what it creates to one identity: a
// worker's fingerprint, or an embedded provider's.
func (c Config) Open(owner string, log *slog.Logger) (Driver, error) {
	switch {
	case c.Docker != nil:
		return docker.New(*c.Docker, owner, log)
	}
	return nil, errors.New("no driver block")
}

// EntryCommand is what the manager execs when a sandbox names no command.
func (c Config) EntryCommand() []string {
	_, b := c.block()
	return b.EntryCommand()
}

// MaxSandboxes is the capacity the manager reports.
func (c Config) MaxSandboxes() int {
	_, b := c.block()
	return b.MaxSandboxes()
}
