package sandbox

import (
	"reflect"
	"runtime"
	"testing"
)

// arch, os and driver are facts about the machine, not settings; the operator adds the
// rest (DESIGN.md decision 8).
func TestTags(t *testing.T) {
	got, err := Tags("docker", []string{" virt:vm ", "gpu", "", "region:eu", "gpu"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"arch:" + runtime.GOARCH, "driver:docker", "gpu",
		"os:" + runtime.GOOS, "region:eu", "virt:vm",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Tags\n got %v\nwant %v (sorted, deduplicated, blanks dropped)", got, want)
	}
	if _, err := Tags("docker", []string{"has space"}); err == nil {
		t.Error("a tag with whitespace must be refused")
	}
}
