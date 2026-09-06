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

	"sandboxd/internal/conf"
	"sandboxd/internal/sandbox/docker"
	"sandboxd/internal/sandbox/drivers"
)

func env(pairs map[string]string) conf.Lookup {
	return func(k string) (string, bool) {
		v, ok := pairs[k]
		return v, ok
	}
}

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFileConfig(t *testing.T) {
	token := writeFile(t, "join", "jt\n")
	path := writeFile(t, "worker.yaml", `
url: wss://cp.example.com//
name: ${HOST_NAME:-box}
tags: [gpu:a100]
join_token_file: `+token+`
identity: /var/lib/sandboxd-worker/host.json
driver:
  docker:
    sock: /run/docker.sock
    max_sandboxes: 2
`)
	cfg, err := Load(path, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		URL:       "wss://cp.example.com", // the tunnel appends /tunnel; a double slash is another route
		Name:      "box",
		Tags:      []string{"arch:" + runtime.GOARCH, "driver:docker", "gpu:a100", "os:" + runtime.GOOS},
		JoinToken: "jt",
		Identity:  "/var/lib/sandboxd-worker/host.json",
		Driver:    drivers.Config{Docker: &docker.Config{Sock: "/run/docker.sock", Entry: "/usr/local/bin/sandboxd-entry", Max: 2}},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("config\n got %+v %+v\nwant %+v %+v", cfg, cfg.Driver.Docker, want, want.Driver.Docker)
	}
	if got := cfg.Redacted().JoinToken; got != "***" {
		t.Errorf("redacted join token = %q", got)
	}
}

func TestFileConfigRejects(t *testing.T) {
	for name, body := range map[string]string{
		"no driver block":                "url: ws://x\n",
		"an unknown key":                 "driver: {docker: {socket: /x}}\n",
		"a bad tag":                      "tags: ['has space']\ndriver: {docker: {}}\n",
		"a driver that is not built yet": "driver: {kubernetes: {}}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeFile(t, "worker.yaml", body), env(nil)); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestLegacyEnvDefaults(t *testing.T) {
	cfg, err := Load("", env(map[string]string{"SANDBOXD_WORKER_CONFIG": "/tmp/host.json"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.URL != defaultURL {
		t.Errorf("URL = %q", cfg.URL)
	}
	if cfg.Identity != "/tmp/host.json" {
		t.Errorf("the old identity variable must still be honoured: %q", cfg.Identity)
	}
	d := cfg.Driver.Docker
	if d == nil || d.Max != 4 || d.Sock != "/var/run/docker.sock" || d.EntryCommand()[0] != "/usr/local/bin/sandboxd-entry" {
		t.Errorf("driver = %+v", d)
	}
}

func TestLegacyEnvIsReadUnchanged(t *testing.T) {
	cfg, err := Load("", env(map[string]string{
		"SANDBOXD_URL":                 "wss://cp.example.com/",
		"SANDBOXD_WORKER_NAME":         "old-box",
		"SANDBOXD_WORKER_MAX_SESSIONS": "9",
		"SANDBOXD_WORKER_ENTRY":        `["bash","-l"]`,
		"SANDBOXD_WORKER_TAGS":         " virt:vm , gpu ,, region:eu, gpu",
		"SANDBOXD_WORKER_IDENTITY":     "/id.json",
		"SANDBOXD_JOIN_TOKEN":          "jt",
		"DOCKER_SOCK":                  "/run/docker.sock",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.URL != "wss://cp.example.com" || cfg.Name != "old-box" || cfg.JoinToken != "jt" || cfg.Identity != "/id.json" {
		t.Errorf("config = %+v", cfg)
	}
	d := cfg.Driver.Docker
	if d.Max != 9 || d.Sock != "/run/docker.sock" || !reflect.DeepEqual(d.EntryCommand(), []string{"bash", "-l"}) {
		t.Errorf("driver = %+v", d)
	}
	want := []string{
		"arch:" + runtime.GOARCH, "driver:docker", "gpu",
		"os:" + runtime.GOOS, "region:eu", "virt:vm",
	}
	if !reflect.DeepEqual(cfg.Tags, want) {
		t.Errorf("Tags\n got %v\nwant %v (sorted, deduplicated, blanks dropped)", cfg.Tags, want)
	}
}

func TestLegacyEnvRejects(t *testing.T) {
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
			_, err := Load("", env(tt.vars))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want one mentioning %q", err, tt.want)
			}
		})
	}
}

// The host file is this machine's identity. A migrated host keeps its fingerprint, so the
// control plane still recognises it and it stays approved.
func TestIdentityIsCreatedOnceAndReused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "host.json")
	first := Config{Identity: path}
	if err := first.LoadIdentity(); err != nil {
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
	if first.Name == "" {
		t.Error("Name should fall back to the hostname")
	}

	second := Config{Identity: path}
	if err := second.LoadIdentity(); err != nil {
		t.Fatal(err)
	}
	if second.Secret != first.Secret || second.Fingerprint != first.Fingerprint {
		t.Error("a second start must reuse the identity on disk")
	}
}

// The shape the TypeScript worker wrote, read back unchanged.
func TestIdentityFromTheTypeScriptWorker(t *testing.T) {
	body := `{"secret": "abcdefgh", "name": "old-box"}`
	path := writeFile(t, "host.json", body)
	cfg := Config{Identity: path}
	if err := cfg.LoadIdentity(); err != nil {
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
	if string(after) != body {
		t.Errorf("host file was rewritten: %s", after)
	}
}

func TestIdentityWithoutASecretIsAnError(t *testing.T) {
	body, _ := json.Marshal(map[string]string{"name": "box"})
	cfg := Config{Identity: writeFile(t, "host.json", string(body))}
	if err := cfg.LoadIdentity(); err == nil {
		t.Error("a host file with no secret has no identity; it must not be silently replaced")
	}
}
