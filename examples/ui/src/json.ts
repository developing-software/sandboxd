// Guards for documents read off the wire or off disk, where every value is `unknown`
// until proven otherwise. The preset loader starts here; so does anything else that reads YAML.

export namespace Json {
  /** A plain object: not null, not an array. Every YAML and JSON reader starts here. */
  export const isObj = (v: unknown): v is Record<string, unknown> =>
    !!v && typeof v === 'object' && !Array.isArray(v)
}
