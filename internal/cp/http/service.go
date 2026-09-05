package http

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// The probe, and the document entry for the socket huma does not serve.

func registerService(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "healthz",
		Method:      http.MethodGet,
		Path:        "/healthz",
		Summary:     "Liveness",
		Tags:        []string{"Service"},
		Security:    []map[string][]string{},
	}, func(_ context.Context, _ *struct{}) (*okOut, error) {
		return &okOut{Body: ok{OK: true}}, nil
	})
}

// documentAttach writes the attach socket into the document by hand. It is a WebSocket
// upgrade with a hijacked connection, so it is not a huma operation — but a client
// reading the document still needs to find it.
func documentAttach(doc *huma.OpenAPI) {
	str := &huma.Schema{Type: "string"}
	num := &huma.Schema{Type: "integer"}
	doc.Paths["/attach"] = &huma.PathItem{
		Get: &huma.Operation{
			OperationID: "attach",
			Summary:     "Attach to a sandbox terminal",
			Description: "Browser endpoint. `token` comes from POST /sandboxes/{id}/attach-token. " +
				"Binary frames are raw PTY bytes both ways; text frames are JSON — the client " +
				"sends {type:\"resize\",cols,rows}, the server sends {type:\"closed\",reason}.",
			Tags:     []string{"Service"},
			Security: []map[string][]string{},
			Parameters: []*huma.Param{
				{Name: "token", In: "query", Required: true, Schema: str},
				{Name: "cols", In: "query", Schema: num, Description: "Default 120."},
				{Name: "rows", In: "query", Schema: num, Description: "Default 40."},
			},
			Responses: map[string]*huma.Response{
				"101": {Description: "WebSocket: binary frames are PTY bytes both ways."},
				"401": {Description: "Invalid or expired attach token."},
			},
		},
	}
}
