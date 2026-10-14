export class ApiError extends Error {
  readonly status: number
  readonly code: string
  /** Seconds from Retry-After, when the server sent it. */
  readonly retryAfter?: number

  constructor(status: number, code: string, message: string, retryAfter?: number) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.retryAfter = retryAfter
  }
}

export function parseRetryAfter(value: string | null): number | undefined {
  if (!value) return undefined
  const seconds = Number(value)
  if (Number.isFinite(seconds)) return Math.max(0, seconds)
  const date = Date.parse(value)
  if (Number.isNaN(date)) return undefined
  return Math.max(0, Math.round((date - Date.now()) / 1000))
}

export async function toApiError(response: Response, body: unknown): Promise<ApiError> {
  const err = (body as { error?: { code?: string; message?: string } } | undefined)?.error
  return new ApiError(
    response.status,
    err?.code ?? `http_${response.status}`,
    err?.message ?? (response.statusText || 'Request failed'),
    parseRetryAfter(response.headers.get('Retry-After')),
  )
}

export function isApiError(e: unknown, code?: string): e is ApiError {
  return e instanceof ApiError && (code === undefined || e.code === code)
}
