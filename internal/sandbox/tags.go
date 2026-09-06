package sandbox

import (
	"fmt"
	"runtime"
	"slices"
	"strings"
)

const maxTagLength = 64

// Tags is what a host reports about itself: `arch:`, `os:` and `driver:` are facts about
// the machine and are not configurable; the operator adds the rest (DESIGN.md decision
// 8). Sorted and deduplicated, so two hosts configured alike report alike.
func Tags(driver string, extra []string) ([]string, error) {
	tags := []string{"arch:" + runtime.GOARCH, "os:" + runtime.GOOS, "driver:" + driver}
	for _, tag := range extra {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if err := ValidTag(tag); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	slices.Sort(tags)
	return slices.Compact(tags), nil
}

// ValidTag is the one rule a tag obeys. A tag is opaque to the control plane, which
// compares strings and nothing else; this exists so a typo fails at boot rather than as
// a sandbox that never places.
func ValidTag(tag string) error {
	if len(tag) > maxTagLength {
		return fmt.Errorf("tag %q is longer than %d characters", tag, maxTagLength)
	}
	if strings.ContainsFunc(tag, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return fmt.Errorf("tag %q contains whitespace or a control character", tag)
	}
	return nil
}
