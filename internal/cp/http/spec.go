package http

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
)

// The OpenAPI document as a file. `go run ./scripts/openapi` writes it to openapi.json at
// the repo root, and the TypeScript SDK is generated from that: the document is a build
// artifact of the handlers in this package and is never hand-edited.

// Spec renders the document without starting anything. The use cases are nil because no
// handler runs — registering an operation only reads its request and response types.
func Spec(publicURL string) ([]byte, error) {
	api := register(http.NewServeMux(), publicURL, nil, nil, slog.New(slog.DiscardHandler))
	raw, err := api.OpenAPI().MarshalJSON()
	if err != nil {
		return nil, err
	}
	// Indented and newline-terminated, because it is checked in and read in diffs.
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}
