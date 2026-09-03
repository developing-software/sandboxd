// apps/api/src/http/doc.ts — the bridge between a Zod schema and its OpenAPI entry.
import { describeRoute, resolver } from 'hono-openapi'
import type { z } from 'zod'
import { ErrorBody } from '../schema'

type Responses = NonNullable<Parameters<typeof describeRoute>[0]['responses']>

/** A JSON body. */
export const json = (description: string, schema: z.ZodType) => ({
  description,
  content: { 'application/json': { schema: resolver(schema) } },
})

const STATUS: Record<number, string> = {
  400: 'Bad Request',
  401: 'Unauthorized',
  404: 'Not Found',
  409: 'Conflict',
  426: 'Upgrade Required',
  500: 'Internal Server Error',
}

/** The several statuses a route can fail with, all as `ErrorBody`. Spread into its responses. */
export const errors = (...codes: number[]): Responses =>
  Object.fromEntries(codes.map((c) => [c, json(STATUS[c] ?? String(c), ErrorBody)]))

/** Every route in a handler shares a tag, so it is bound once at the top of the file. */
export const describe =
  (tag: string) => (summary: string, responses: Responses, description?: string) =>
    describeRoute({ tags: [tag], summary, description, responses })
