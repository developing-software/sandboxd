export class HttpError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message)
  }
}
export const notFound = (what: string) => new HttpError(404, `${what} not found`)
export const badRequest = (msg: string) => new HttpError(400, msg)
export const unauthorized = (msg = 'unauthorized') => new HttpError(401, msg)
export const conflict = (msg: string) => new HttpError(409, msg)
