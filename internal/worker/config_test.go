package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

func TestLoadConfigDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.json")
	cfg, err := LoadConfig(env(map[string]string{"SANDBOXD_WORKER_CONFIG": path}))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.URL != defaultURL {
		t.Errorf("URL = %q", cfg.URL)
	}
	if cfg.MaxSandboxes != defaultMax {
		t.Errorf("MaxSandboxes = %d", cfg.MaxSandboxes)
	}
	if !reflect.DeepEqual(cfg.Entry, []string{defaultEntry}) {
		t.Errorf("Entry = %v", cfg.Entry)
	}
	if cfg.DockerSock != defaultSock || cfg.Driver != DriverDocker {
		t.Errorf("DockerSock = %q, Driver = %q", cfg.DockerSock, cfg.Driver)
	}
	if cfg.Name == "" {
		t.Error("Name should fall back to the hostname")
	}
}

// The host file is this machine's identity. A migrated host keeps its fingerprint, so the
// control plane still recognises it and it stays approved.
func TestHostFileIsCreatedOnceAndReused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "host.json")
	getenv := env(map[string]string{"SANDBOXD_WORKER_CONFIG": path})

	first, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != configFileMode {
		t.Errorf("mode = %v, want %v", info.Mode().Perm(), configFileMode)
	}

	sum := sha256.Sum256([]byte(first.Secret))
	if first.Fingerprint != hex.EncodeToString(sum[:]) {
		t.Error("the fingerprint is sha256 of the secret, hex")
	}
	if strings.Contains(first.Fingerprint, first.Secret) {
		t.Error("the fingerprint must not carry the secret")
	}

	second, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if second.Secret != first.Secret || second.Fingerprint != first.Fingerprint {
		t.Error("a second start must reuse the identity on disk")
	}
}

// The shape the TypeScript worker wrote, read back unchanged.
func TestHostFileFromTheTypeScriptWorker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.json")
	body := []byte(`{"secret": "abcdefgh", "name": "old-box"}`)
	if err := os.WriteFile(path, body, configFileMode); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(env(map[string]string{"SANDBOXD_WORKER_CONFIG": path}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Secret != "abcdefgh" || cfg.Name != "old-box" {
		t.Errorf("got secret %q name %q", cfg.Secret, cfg.Name)
	}

	// And is not rewritten under the worker's feet.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(body) {
		t.Errorf("host file was rewritten: %s", after)
	}
}

func TestLoadConfigRejects(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		vars map[string]string
		want string
	}{
		{"a max that is not a number", map[string]string{"SANDBOXD_WORKER_MAX_SESSIONS": "many"}, "MAX_SESSIONS"},
		{"a max of zero", map[string]string{"SANDBOXD_WORKER_MAX_SESSIONS": "0"}, "MAX_SESSIONS"},
		{"a driver that is not built yet", map[string]string{"SANDBOXD_WORKER_DRIVER": "kubernetes"}, "not implemented"},
		{"a tag with a space", map[string]string{"SANDBOXD_WORKER_TAGS": "gpu, has space"}, "whitespace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.vars["SANDBOXD_WORKER_CONFIG"] = filepath.Join(dir, "host.json")
			_, err := LoadConfig(env(tt.vars))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want one mentioning %q", err, tt.want)
			}
		})
	}
}

// arch, os and driver are facts about the machine, not settings; the operator adds the
// rest (SPEC.md, "Tags").
func TestTags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.json")
	cfg, err := LoadConfig(env(map[string]string{
		"SANDBOXD_WORKER_CONFIG": path,
		"SANDBOXD_WORKER_TAGS":   " virt:vm , gpu ,, region:eu, gpu",
	}))
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"arch:" + runtime.GOARCH, "driver:docker", "gpu",
		"os:" + runtime.GOOS, "region:eu", "virt:vm",
	}
	if !reflect.DeepEqual(cfg.Tags, want) {
		t.Errorf("Tags\n got %v\nwant %v (sorted, deduplicated, blanks dropped)", cfg.Tags, want)
	}
}

func TestEntryParsing(t *testing.T) {
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
		if got := parseEntry(tt.raw); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("parseEntry(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}

func TestConfigURLLosesItsTrailingSlash(t *testing.T) {
	cfg, err := LoadConfig(env(map[string]string{
		"SANDBOXD_WORKER_CONFIG": filepath.Join(t.TempDir(), "host.json"),
		"SANDBOXD_URL":           "wss://cp.example.com//",
	}))
	if err != nil {
		t.Fatal(err)
	}
	// The tunnel appends /tunnel; a double slash is a different route.
	if cfg.URL != "wss://cp.example.com" {
		t.Errorf("URL = %q", cfg.URL)
	}
}

func TestHostFileWithoutASecretIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.json")
	body, _ := json.Marshal(map[string]string{"name": "box"})
	if err := os.WriteFile(path, body, configFileMode); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(env(map[string]string{"SANDBOXD_WORKER_CONFIG": path})); err == nil {
		t.Error("a host file with no secret has no identity; it must not be silently replaced")
	}
}
