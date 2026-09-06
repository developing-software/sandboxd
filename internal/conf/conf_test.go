package conf

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type inner struct {
	Sock string `yaml:"sock"`
	Max  int    `yaml:"max_sandboxes"`
}

type testConfig struct {
	Listen string            `yaml:"listen"`
	Port   int               `yaml:"port"`
	Tags   []string          `yaml:"tags"`
	Env    map[string]string `yaml:"env"`
	Docker *inner            `yaml:"docker"`
	Flat   inner             `yaml:",inline"`
	Hidden string            `yaml:"-"`
}

func env(pairs map[string]string) Lookup {
	return func(k string) (string, bool) {
		v, ok := pairs[k]
		return v, ok
	}
}

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadSubstitutesInValuesOnly(t *testing.T) {
	path := write(t, `
listen: ${LISTEN}
port: ${PORT}
tags: [a, "$LITERAL", "${MISSING:-dflt}", "${SET:?must be set}"]
env:
  ${NOT_A_KEY}: x$$y
docker:
  sock: "${PORT}"
sock: /var/run/docker.sock
`)
	cfg, err := Load[testConfig](path, env(map[string]string{
		"LISTEN": ":9000", "PORT": "9000", "SET": "yes",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := testConfig{
		Listen: ":9000",
		Port:   9000, // a plain scalar that became a number decodes as one
		Tags:   []string{"a", "$LITERAL", "dflt", "yes"},
		Env:    map[string]string{"${NOT_A_KEY}": "x$y"},
		Docker: &inner{Sock: "9000"}, // quoted stays a string
		Flat:   inner{Sock: "/var/run/docker.sock"},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("got %+v\nwant %+v", cfg, want)
	}
}

// Three faults, one call, each with a line: an unset variable, an unknown key, a wrong
// type. Fixing them one boot at a time is the failure mode this exists to avoid.
func TestLoadReportsEveryFaultWithItsLine(t *testing.T) {
	path := write(t, `listen: ${NOPE}
docker:
  socket: /x
port: many
`)
	_, err := Load[testConfig](path, env(nil))
	if err == nil {
		t.Fatal("expected errors")
	}
	msg := err.Error()
	for _, want := range []string{
		"api.yaml:1:9: ${NOPE} is not set",
		`api.yaml:3:3: unknown key "socket" in docker`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error lacks %q:\n%s", want, msg)
		}
	}
	// The type fault is only found once the shape is right, and it names its line too.
	path = write(t, "listen: x\nport: many\n")
	if _, err := Load[testConfig](path, env(nil)); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("type error = %v, want one naming line 2", err)
	}
}

func TestSubstitutionGrammar(t *testing.T) {
	lookup := env(map[string]string{"A": "a", "EMPTY": ""})
	for in, want := range map[string]string{
		"${A}":           "a",
		"x${A}y":         "xay",
		"$$":             "$",
		"$$${A}":         "$a",
		"$A":             "$A", // bare: not expanded
		"a$":             "a$",
		"${EMPTY}":       "", // set to empty is the operator's choice
		"${EMPTY:-d}":    "d",
		"${B:-}":         "",
		"${B:-x:y}":      "x:y",
		"${A:?required}": "a",
	} {
		got, err := substitute(in, lookup)
		if err != nil || got != want {
			t.Errorf("substitute(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for in, want := range map[string]string{
		"${B}":             "${B} is not set",
		"${EMPTY:?set me}": "${EMPTY} set me",
		"${B:?}":           "${B} is not set",
		"${A":              "unterminated",
		"${1A}":            "not a variable name",
		"${A:x}":           "expected :- or :?",
	} {
		_, err := substitute(in, lookup)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("substitute(%q) = %v, want an error mentioning %q", in, err, want)
		}
	}
}

func TestSecretTwins(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("  s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Secret("auth.service_token", "", file); err != nil || got != "s3cret" {
		t.Errorf("from file = %q, %v", got, err)
	}
	if got, err := Secret("auth.service_token", "inline", ""); err != nil || got != "inline" {
		t.Errorf("inline = %q, %v", got, err)
	}
	if _, err := Secret("auth.service_token", "inline", file); err == nil {
		t.Error("both set must be refused")
	}
	if _, err := Secret("auth.service_token", "", "/nope"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file = %v", err)
	}
}

func TestLocate(t *testing.T) {
	dir := t.TempDir()
	present := write(t, "listen: x\n")
	if got, err := Locate("", env(nil), filepath.Join(dir, "none.yaml")); err != nil || got != "" {
		t.Errorf("no file anywhere = %q, %v; want the legacy path", got, err)
	}
	if got, err := Locate("", env(nil), present); err != nil || got != present {
		t.Errorf("default present = %q, %v", got, err)
	}
	if got, err := Locate("", env(map[string]string{EnvVar: present}), "/none"); err != nil || got != present {
		t.Errorf("from the environment = %q, %v", got, err)
	}
	if _, err := Locate(filepath.Join(dir, "typo.yaml"), env(nil), present); err == nil {
		t.Error("a path given explicitly must exist")
	}
}

func TestIgnored(t *testing.T) {
	got := Ignored([]string{"SANDBOXD_PORT=1", "HOME=/", "SANDBOXD_CONFIG=/x", "SANDBOXD_DB=y"})
	if !reflect.DeepEqual(got, []string{"SANDBOXD_DB", "SANDBOXD_PORT"}) {
		t.Errorf("ignored = %v", got)
	}
}
