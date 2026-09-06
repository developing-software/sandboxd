import { defineConfig } from '@hey-api/openapi-ts'

// The fleet half of this app, generated here rather than imported.
//
// `@sandboxd/sdk` is the client contract and nothing else: enrolling workers and listing
// hosts live in `api/admin.yaml`, which has no published client so that it can break
// whenever the operator is better served (DESIGN.md decision 27). This app wants both
// halves, so it pays the price of the split — one config, one generated tree — and the
// churn lands here instead of in a parent app's SDK.
//
// The client half still comes from the package: nothing about `/sandboxes` is generated
// in this repo twice.
export default defineConfig({
  input: '../../api/admin.yaml',
  // Checked in, and neither the formatter nor the linter touches it, exactly as
  // sdk/typescript treats its own output.
  output: { path: 'src/admin', postProcess: [] },
  plugins: ['@hey-api/client-fetch', '@hey-api/typescript', '@hey-api/sdk'],
})
