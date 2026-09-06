// Package api is the two OpenAPI documents and nothing else. They are written by hand and
// everything else is generated from them: ogen's server and types below, hey-api's
// TypeScript client in sdk/typescript (DESIGN.md decisions 22 and 27).
//
// The package exists so the control plane can serve the documents it was generated from,
// rather than a second rendering of them that could disagree. It imports `embed` and
// nothing else, ours or otherwise.
package api

import _ "embed"

// ogen runs from this directory, so the paths below are the ones a reader sees beside
// them. `-clean` empties each target first: a file left behind by a rename would still
// compile and would still be wrong.
//
//go:generate go tool ogen -target ../internal/gen/clientapi -package clientapi -config ogen.yml -clean client.yaml
//go:generate go tool ogen -target ../internal/gen/adminapi -package adminapi -config ogen.yml -clean admin.yaml

// Client is the contract: sandboxes, the terminal, previews, the probe. A published SDK
// comes from it.
//
//go:embed client.yaml
var Client []byte

// Admin is the operator surface: enrolling workers and looking at the fleet. No published
// client, and free to break.
//
//go:embed admin.yaml
var Admin []byte
