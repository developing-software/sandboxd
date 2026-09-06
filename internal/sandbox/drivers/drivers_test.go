package drivers

import (
	"testing"

	"sandboxd/internal/sandbox/docker"
)

func TestExactlyOneBlock(t *testing.T) {
	var none Config
	if err := none.Validate(); err == nil || none.Name() != "" {
		t.Errorf("an empty config must be refused, got %v %q", err, none.Name())
	}
	if _, err := none.Open("o", nil); err == nil {
		t.Error("nothing to open")
	}
	one := Config{Docker: &docker.Config{Max: 2}}
	if err := one.Validate(); err != nil {
		t.Fatal(err)
	}
	if one.Name() != "docker" || one.MaxSandboxes() != 2 || one.EntryCommand()[0] == "" {
		t.Errorf("docker block = %q %d %v", one.Name(), one.MaxSandboxes(), one.EntryCommand())
	}
	bad := Config{Docker: &docker.Config{Max: -1}}
	if err := bad.Validate(); err == nil {
		t.Error("the block's own validation must run")
	}
}
