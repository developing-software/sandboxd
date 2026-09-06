package cp

import (
	"maps"
	"regexp"
	"slices"
)

// Rules for a caller-supplied env map, applied identically to `env` and `secret_env`.
// They are semantics an OpenAPI schema cannot state — a reserved name, a byte budget
// across keys and values — so they run here rather than in the generated validator.

// reserved names are set by the worker on every PTY; a caller may not override them.
var reserved = map[string]struct{}{
	"TERM":                {},
	"SANDBOXD_SESSION_ID": {},
}

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// maxEnvBytes caps keys plus values. Large inputs belong in the repo or the image.
const maxEnvBytes = 64 << 10

// ValidateEnv reports the first problem with a caller's env map, named so the message
// says which of the two maps was wrong.
func ValidateEnv(name string, env map[string]string) error {
	bytes := 0
	// Sorted, so two runs against the same bad map report the same variable.
	for _, k := range slices.Sorted(maps.Keys(env)) {
		v := env[k]
		if !envKey.MatchString(k) {
			return Invalid("%s: invalid variable name %q", name, k)
		}
		if _, bad := reserved[k]; bad {
			return Invalid("%s: %q is reserved", name, k)
		}
		bytes += len(k) + len(v)
	}
	if bytes > maxEnvBytes {
		return Invalid(
			"%s exceeds %d bytes; ship large inputs through the repo or the image",
			name, maxEnvBytes)
	}
	return nil
}
