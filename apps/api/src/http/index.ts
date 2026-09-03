// apps/api/src/http/index.ts — the app, plus the two endpoints that document it.
import { generateSpecs } from 'hono-openapi'
import { createRoutes, type Deps } from './routes'

export function createApp(deps: Deps) {
  const app = createRoutes(deps)

  // Generated once per process: walking every route and converting every schema is not
  // free, and the result only changes when the code does.
  let cached: ReturnType<typeof generateSpecs> | undefined
  const spec = () =>
    (cached ??= generateSpecs(app, {
      documentation: {
        info: {
          title: 'sandboxd control plane',
          description:
            'Generic sandboxes on your own hosts. Every operation is declared in apps/api/src/schema.ts; presets live in apps/ui.',
          version: '1.0.0',
        },
        components: { securitySchemes: { Bearer: { type: 'http', scheme: 'bearer' } } },
        security: [{ Bearer: [] }],
      },
    }))

  // `servers` is filled in per request so the same process self-describes on any host name.
  app.get('/openapi.json', async (c) =>
    c.json({ ...(await spec()), servers: [{ url: new URL(c.req.url).origin }] }),
  )
  app.get('/doc', (c) =>
    c.html(`<!doctype html>
<html>
  <head>
    <title>sandboxd API</title>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
  </head>
  <body>
    <div id="app"></div>
    <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
    <script>Scalar.createApiReference('#app', { url: '/openapi.json' })</script>
  </body>
</html>`),
  )
  return app
}
