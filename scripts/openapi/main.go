// Command openapi writes the control plane's OpenAPI document. `go run ./scripts/openapi`
// refreshes openapi.json at the repo root, which is what the TypeScript SDK in
// sdk/typescript is generated from. A test in internal/cp/http fails when the file and
// the handlers disagree, so this is the only way the document changes.
//
// It is a development tool, not part of the product: nix/packages.nix builds only cmd/.
package main

import (
	"flag"
	"fmt"
	"os"

	cphttp "sandboxd/internal/cp/http"
)

// defaultServer is the URL the checked-in document advertises. A deployed control plane
// serves its own at /openapi.json with SANDBOXD_PUBLIC_URL in this slot; the file is for
// generators, which take the base URL from their caller.
const defaultServer = "http://localhost:8080"

func main() {
	server := flag.String("server", defaultServer, "the `url` the document lists as its server")
	out := flag.String("o", "", "write to this `file` instead of stdout")
	flag.Parse()

	if err := run(*server, *out); err != nil {
		fmt.Fprintln(os.Stderr, "openapi:", err)
		os.Exit(1)
	}
}

func run(server, out string) error {
	doc, err := cphttp.Spec(server)
	if err != nil {
		return err
	}
	if out == "" {
		_, err = os.Stdout.Write(doc)
		return err
	}
	return os.WriteFile(out, doc, 0o644)
}
