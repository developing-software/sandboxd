package http

import (
	"bytes"
	"os"
	"testing"
)

// checkedIn is the document the TypeScript SDK is generated from, relative to this
// package. It is at the repo root so a generator config can name one obvious path.
const checkedIn = "../../../openapi.json"

// The document is generated, not written, so the only way it can be wrong is by being
// stale. This is the gate that says so, in the same `go test ./...` everything else runs in.
func TestSpecMatchesCheckedIn(t *testing.T) {
	want, err := os.ReadFile(checkedIn)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Spec(specServer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("openapi.json is stale: run `go run ./scripts/openapi -o openapi.json`")
	}
}

// specServer must match scripts/openapi's default, which is the URL the checked-in
// document lists.
const specServer = "http://localhost:8080"

// A generated client should see no `$schema` on a body it sends or receives: huma's link
// transformer is off, and turning it back on would type a field no caller has.
func TestSpecHasNoSchemaLinks(t *testing.T) {
	doc, err := Spec(specServer)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(doc, []byte(`"$schema"`)) {
		t.Error("the document declares a $schema property; huma's create hook is back")
	}
}
