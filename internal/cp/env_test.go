package cp

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateEnv(t *testing.T) {
	cases := map[string]struct {
		env  map[string]string
		want string // a fragment of the message, empty when the map is fine
	}{
		"empty":                {nil, ""},
		"ordinary":             {map[string]string{"A_1": "x", "_LEADING": ""}, ""},
		"a dash is not a name": {map[string]string{"bad-name": "x"}, "invalid variable name"},
		"a digit cannot lead":  {map[string]string{"1A": "x"}, "invalid variable name"},
		"TERM is the worker's": {map[string]string{"TERM": "x"}, `"TERM" is reserved`},
		"so is the sandbox id": {map[string]string{"SANDBOXD_SESSION_ID": "x"}, "is reserved"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateEnv("env", c.env)
			if c.want == "" {
				if err != nil {
					t.Fatalf("err = %v, want none", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, c.want)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("err = %v, want an ErrInvalid so http answers 400", err)
			}
		})
	}
}

func TestValidateEnvCapsTheWholeMap(t *testing.T) {
	big := map[string]string{"BIG": strings.Repeat("x", maxEnvBytes)}
	err := ValidateEnv("secret_env", big)
	if err == nil || !strings.Contains(err.Error(), "secret_env exceeds") {
		t.Fatalf("err = %v", err)
	}
	// Named, because the message has to say which of the two maps was too big.
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("err = %v, want ErrInvalid", err)
	}
}

func TestValidateEnvReportsTheSameVariableEveryRun(t *testing.T) {
	// Go map order is random; a caller debugging a bad deploy must not get a different
	// answer on the second call.
	env := map[string]string{"bad-one": "x", "bad-two": "y", "bad-three": "z"}
	first := ValidateEnv("env", env).Error()
	for range 20 {
		if got := ValidateEnv("env", env).Error(); got != first {
			t.Fatalf("two runs disagree: %q then %q", first, got)
		}
	}
}
