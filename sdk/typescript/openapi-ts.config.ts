import { defineConfig } from '@hey-api/openapi-ts'

// The input is the hand-written contract, not a running control plane and not a rendering
// of the Go handlers: `api/client.yaml` is the source both generators read, and the Go
// server in `internal/gen/clientapi` comes from the same file (DESIGN.md decision 22).
//
// `api/admin.yaml` is deliberately not here. The operator surface has no published client,
// which is what lets it break whenever the operator is better served (decision 27).
export default defineConfig({
  input: '../../api/client.yaml',
  // No post-processing, and the formatter and linter ignore src/generated: the output is
  // reproduced byte for byte by `bun run generate`, so a diff there is a change to the
  // API and nothing else.
  output: { path: 'src/generated', postProcess: [] },
  plugins: ['@hey-api/client-fetch', '@hey-api/typescript', '@hey-api/sdk'],
})
