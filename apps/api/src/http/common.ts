// apps/api/src/http/common.ts
import type { ValidationTargets } from 'hono'
import type { z } from 'zod'
import { validator as standardValidator } from 'hono-openapi'
import type { ErrorBody } from '../schema'

/** hono-openapi's validator, reporting failures as the API's one error shape. */
export const validator = <S extends z.ZodType, T extends keyof ValidationTargets>(
  target: T,
  schema: S,
) =>
  standardValidator(target, schema, (result, c) => {
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
