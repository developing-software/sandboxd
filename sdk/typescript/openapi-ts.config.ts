import { defineConfig } from '@hey-api/openapi-ts'

// The input is the checked-in document, not a running control plane: `go run
// ./scripts/openapi -o openapi.json` refreshes it and a Go test fails when it is stale,
// so generating here never needs a server or a network.
export default defineConfig({
  input: '../../openapi.json',
  // No post-processing, and the formatter and linter ignore src/generated: the output is
  // reproduced byte for byte by `bun run generate`, so a diff there is a change to the
  // API and nothing else.
  output: { path: 'src/generated', postProcess: [] },
  plugins: ['@hey-api/client-fetch', '@hey-api/typescript', '@hey-api/sdk'],
})
