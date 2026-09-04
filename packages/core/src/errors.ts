export namespace Err {
  export class Http extends Error {
    constructor(
      public status: number,
      message: string,
    ) {
      super(message)
    }
  }

  export const notFound = (what: string) => new Http(404, `${what} not found`)
  export const badRequest = (msg: string) => new Http(400, msg)
  export const unauthorized = (msg = 'unauthorized') => new Http(401, msg)
  export const conflict = (msg: string) => new Http(409, msg)
}
