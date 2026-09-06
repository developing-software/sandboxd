package docker

import (
	"reflect"
	"testing"
)

func TestEntryCommand(t *testing.T) {
	tests := []struct {
		raw  string
		want []string
	}{
		// What the NixOS module writes.
		{`["/usr/local/bin/sandboxd-entry"]`, []string{"/usr/local/bin/sandboxd-entry"}},
		{`["bash","-lc","echo hi"]`, []string{"bash", "-lc", "echo hi"}},
		{`bash -l`, []string{"bash", "-l"}},
		{`  /entry  `, []string{"/entry"}},
		// Not an array after all: treated as a command line rather than failing to boot.
		{`[oops`, []string{"[oops"}},
	}
	for _, tt := range tests {
		if got := (Config{Entry: tt.raw}).EntryCommand(); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("EntryCommand(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}

func TestConfigDefaults(t *testing.T) {
	var cfg Config
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	want := Config{Sock: defaultSock, Entry: defaultEntry, Max: defaultMax}
	if cfg != want {
		t.Errorf("defaults = %+v, want %+v", cfg, want)
	}
	if err := (&Config{Max: -1}).Validate(); err == nil {
		t.Error("a negative max must be refused")
	}
}
