export namespace Log {
  export type Logger = ReturnType<typeof create>

  export const create = (scope: string) => {
    const fmt = (level: string, msg: string, extra?: unknown) =>
      `${new Date().toISOString()} ${level.padEnd(5)} [${scope}] ${msg}${extra === undefined ? '' : ' ' + JSON.stringify(extra)}`
    return {
      info: (msg: string, extra?: unknown) => console.log(fmt('info', msg, extra)),
      warn: (msg: string, extra?: unknown) => console.warn(fmt('warn', msg, extra)),
      error: (msg: string, extra?: unknown) => console.error(fmt('error', msg, extra)),
    }
  }
}
