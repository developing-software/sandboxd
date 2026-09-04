// The bridge between a Zod schema and its OpenAPI entry. The validator lives here too:
// a route declares its document and its 400s from the same schema, in one chain.
import type { ValidationTargets } from 'hono'
import type { z } from 'zod'
import { describeRoute, resolver, validator as standard } from 'hono-openapi'
import { ErrorBody } from '../schema'

type Responses = NonNullable<Parameters<typeof describeRoute>[0]['responses']>

const STATUS: Record<number, string> = {
  400: 'Bad Request',
  401: 'Unauthorized',
  404: 'Not Found',
  409: 'Conflict',
  426: 'Upgrade Required',
  500: 'Internal Server Error',
}

export namespace Doc {
  /** A JSON body. */
  export const json = (description: string, schema: z.ZodType) => ({
    description,
    content: { 'application/json': { schema: resolver(schema) } },
  })

  /** The several statuses a route can fail with, all as `ErrorBody`. Spread into its responses. */
  export const errors = (...codes: number[]): Responses =>
    Object.fromEntries(codes.map((c) => [c, json(STATUS[c] ?? String(c), ErrorBody)]))

  /** Every route in a handler shares a tag, so it is bound once at the top of the file. */
  export const tag =
    (name: string) => (summary: string, responses: Responses, description?: string) =>
      describeRoute({ tags: [name], summary, description, responses })

  /** hono-openapi's validator, reporting failures as the API's one error shape. */
  export const validator = <S extends z.ZodType, T extends keyof ValidationTargets>(
    target: T,
    schema: S,
  ) =>
    standard(target, schema, (result, c) => {
      if (result.success) return
      // One entry per failing field, so a client can mark up a whole form from one response.
      const issues = result.error.map((issue) => ({
        path: issue.path?.map(String).join('.') || undefined,
        message: issue.message,
      }))
      const first = issues[0]
      const error = !first
        ? 'invalid request'
        : first.path && !first.message.startsWith(first.path)
          ? `${first.path}: ${first.message}`
          : first.message
      return c.json({ error, issues } satisfies z.infer<typeof ErrorBody>, 400)
    })
}
