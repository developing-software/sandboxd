// Package conf loads a daemon's one YAML file: strict, with `${VAR}` substitution in
// values and `_file` twins for secrets. A leaf — it knows the file's grammar and nothing
// about what either daemon puts in it.
package conf

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/go-faster/yaml"
)

// EnvVar names the file when no flag does.
const EnvVar = "SANDBOXD_CONFIG"

// Lookup is os.LookupEnv's shape: the bool is what tells unset from empty.
type Lookup func(string) (string, bool)

// Locate is the search order: the flag, then SANDBOXD_CONFIG, then the daemon's default
// path. A path given explicitly must exist; the default may not, and then the daemon
// falls back to the legacy environment variables with an empty path.
func Locate(flag string, lookup Lookup, fallback string) (string, error) {
	path := flag
	if path == "" {
		path, _ = lookup(EnvVar)
	}
	if path != "" {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("config: %w", err)
		}
		return path, nil
	}
	if _, err := os.Stat(fallback); err == nil {
		return fallback, nil
	}
	return "", nil
}

// Load reads path into T. Every fault found is reported — a missing variable, an unknown
// key, a value of the wrong type — each with the line it is on, so one boot fixes them all.
func Load[T any](path string, lookup Lookup) (T, error) {
	var out T
	raw, err := os.ReadFile(path)
	if err != nil {
		return out, fmt.Errorf("config: %w", err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return out, fmt.Errorf("config: %s: %w", path, err)
	}
	name := filepath.Base(path)
	var errs []error
	errs = append(errs, expand(&root, lookup, name)...)
	errs = append(errs, unknownKeys(&root, reflect.TypeOf(out), "", name)...)
	if len(errs) > 0 {
		return out, errors.Join(errs...)
	}
	if root.Kind != 0 {
		if err := root.Decode(&out); err != nil {
			return out, fmt.Errorf("config: %s: %w", name, err)
		}
	}
	return out, nil
}

// Secret resolves a value and its `_file` twin: one of them, read and trimmed if it is
// the file. Both set is a fault, because two sources for one secret is one too many.
func Secret(key, value, file string) (string, error) {
	switch {
	case value != "" && file != "":
		return "", fmt.Errorf("%s and %s_file are both set", key, key)
	case file == "":
		return value, nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("%s_file: %w", key, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// Ignored lists the SANDBOXD_* variables in environ that a config file makes irrelevant:
// with a file, the environment reaches the configuration only through `${VAR}`.
func Ignored(environ []string) []string {
	var out []string
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "SANDBOXD_") && k != EnvVar {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// Print renders v as YAML, for --check-config. The caller redacts first.
func Print[T any](w io.Writer, v T) error {
	b, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// --- substitution ------------------------------------------------------------------

// expand substitutes in every scalar value, never in a key. It runs on the node tree so
// a value that becomes a number still decodes as one, and a fault still names a line.
func expand(n *yaml.Node, lookup Lookup, name string) []error {
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		var errs []error
		for _, c := range n.Content {
			errs = append(errs, expand(c, lookup, name)...)
		}
		return errs
	case yaml.MappingNode:
		var errs []error
		for i := 1; i < len(n.Content); i += 2 {
			errs = append(errs, expand(n.Content[i], lookup, name)...)
		}
		return errs
	case yaml.ScalarNode:
		if !strings.Contains(n.Value, "$") {
			return nil
		}
		v, err := substitute(n.Value, lookup)
		if err != nil {
			return []error{fmt.Errorf("%s:%d:%d: %w", name, n.Line, n.Column, err)}
		}
		// A plain scalar was tagged by what it looked like before substitution; clearing
		// the tag lets `8080` decode as the number it now is. A quoted one stays a string.
		if n.Style == 0 {
			n.Tag = ""
		}
		n.Value = v
	}
	return nil
}

// substitute expands `${VAR}`, `${VAR:-default}`, `${VAR:?message}` and `$$`. A bare
// `$HOME` passes through untouched, and an unset variable with no default is a fault.
func substitute(s string, lookup Lookup) (string, error) {
	var out strings.Builder
	for {
		i := strings.IndexByte(s, '$')
		if i < 0 || i == len(s)-1 {
			out.WriteString(s)
			return out.String(), nil
		}
		out.WriteString(s[:i])
		s = s[i+1:]
		switch s[0] {
		case '$':
			out.WriteByte('$')
			s = s[1:]
		case '{':
			end := strings.IndexByte(s, '}')
			if end < 0 {
				return "", fmt.Errorf("unterminated ${ in %q", "$"+s)
			}
			v, err := lookupExpr(s[1:end], lookup)
			if err != nil {
				return "", err
			}
			out.WriteString(v)
			s = s[end+1:]
		default:
			out.WriteByte('$')
		}
	}
}

func lookupExpr(expr string, lookup Lookup) (string, error) {
	name, rest, hasOp := strings.Cut(expr, ":")
	if !validName(name) {
		return "", fmt.Errorf("${%s}: not a variable name", expr)
	}
	v, set := lookup(name)
	if !hasOp {
		if !set {
			return "", fmt.Errorf("${%s} is not set", name)
		}
		return v, nil
	}
	if rest == "" {
		return "", fmt.Errorf("${%s}: expected :- or :? after the name", expr)
	}
	switch rest[0] {
	case '-':
		if v == "" {
			return rest[1:], nil
		}
		return v, nil
	case '?':
		if v == "" {
			msg := rest[1:]
			if msg == "" {
				msg = "is not set"
			}
			return "", fmt.Errorf("${%s} %s", name, msg)
		}
		return v, nil
	}
	return "", fmt.Errorf("${%s}: expected :- or :? after the name", expr)
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		alpha := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !alpha && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// --- strictness --------------------------------------------------------------------

// unknownKeys walks the document beside the type it will decode into and reports every
// key the type has no field for, with its position. The decoder's own strict mode cannot
// run on a node tree, and this is the one rule of its that we need.
func unknownKeys(n *yaml.Node, t reflect.Type, path, name string) []error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil
		}
		return unknownKeys(n.Content[0], t, path, name)
	case yaml.SequenceNode:
		if t.Kind() != reflect.Slice && t.Kind() != reflect.Array {
			return nil
		}
		var errs []error
		for _, c := range n.Content {
			errs = append(errs, unknownKeys(c, t.Elem(), path, name)...)
		}
		return errs
	case yaml.MappingNode:
		var errs []error
		switch t.Kind() {
		case reflect.Map:
			for i := 1; i < len(n.Content); i += 2 {
				errs = append(errs, unknownKeys(n.Content[i], t.Elem(), path, name)...)
			}
		case reflect.Struct:
			fields := fieldsOf(t)
			for i := 0; i+1 < len(n.Content); i += 2 {
				k := n.Content[i]
				f, ok := fields[k.Value]
				if !ok {
					errs = append(errs, fmt.Errorf("%s:%d:%d: unknown key %q%s",
						name, k.Line, k.Column, k.Value, in(path)))
					continue
				}
				errs = append(errs, unknownKeys(n.Content[i+1], f, join(path, k.Value), name)...)
			}
		}
		return errs
	}
	return nil
}

// fieldsOf maps yaml keys to field types the way the decoder does: the tag's name, else
// the lowercased field name; `-` skips; `inline` flattens an embedded struct.
func fieldsOf(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		key, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if key == "-" {
			continue
		}
		if strings.Contains(opts, "inline") {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				for k, v := range fieldsOf(ft) {
					out[k] = v
				}
			}
			continue
		}
		if key == "" {
			key = strings.ToLower(f.Name)
		}
		out[key] = f.Type
	}
	return out
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func in(path string) string {
	if path == "" {
		return ""
	}
	return " in " + path
}
