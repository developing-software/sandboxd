// The browser's one road to the control plane: JSON over this app's /api, which forwards
// with the token added. A failing call throws the API's own message.
import type { ErrorModel } from '@sandboxd/sdk'
import { Shell } from './shell'

export namespace Client {
  export const get = <T>(path: string) => call<T>('GET', path)
  export const post = <T>(path: string, body?: unknown) => call<T>('POST', path, body)
  export const del = <T>(path: string) => call<T>('DELETE', path)
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api${path}`, {
    method,
    // The owner rides on every call rather than in each caller's URL or body, the same
    // way it does on the API this app wraps. /api/presets and /api/hosts ignore it.
    headers: {
      'x-sandboxd-owner': Shell.owner(),
      ...(body === undefined ? {} : { 'content-type': 'application/json' }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (res.status === 204) return undefined as T
  const json: unknown = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error((json as Partial<ErrorModel>).error ?? `${res.status}`)
  return json as T
}
