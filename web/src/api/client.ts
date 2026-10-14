import createClient from 'openapi-fetch'
import type { paths } from './schema'
import { toApiError } from './errors'

/** The backend rejects state-changing calls without this header (403 csrf). */
export const CSRF = { 'X-Requested-With': 'anora' } as const

type UnauthorizedHandler = () => Promise<boolean>
let onUnauthorized: UnauthorizedHandler | null = null
let reauth: Promise<boolean> | null = null

/** AuthGate registers how to recover a session after a 401 (section 3 of the spec). */
export function setUnauthorizedHandler(handler: UnauthorizedHandler | null) {
  onUnauthorized = handler
}

const NO_REAUTH = ['/api/v1/auth/', '/api/v1/me']

async function authFetch(request: Request): Promise<Response> {
  const retry = request.clone()
  const response = await fetch(request)
  const path = new URL(request.url).pathname
  if (response.status !== 401 || !onUnauthorized || NO_REAUTH.some((p) => path.startsWith(p))) {
    return response
  }
  // One re-auth for all concurrent 401s, then one retry per request.
  reauth ??= onUnauthorized().finally(() => {
    reauth = null
  })
  return (await reauth) ? fetch(retry) : response
}

export const api = createClient<paths>({
  baseUrl: typeof window === 'undefined' ? '' : window.location.origin,
  credentials: 'include',
  fetch: authFetch,
})

api.use({
  onRequest({ request }) {
    if (request.method !== 'GET' && request.method !== 'HEAD') request.headers.set('X-Requested-With', 'anora')
    return request
  },
})

type FetchResult<T> = { data?: T; error?: unknown; response: Response }

/** Unwraps an openapi-fetch result: data on 2xx, a typed ApiError otherwise. */
export async function call<T>(promise: Promise<FetchResult<T>>): Promise<T> {
  const { data, error, response } = await promise
  if (response.ok) return data as T
  throw await toApiError(response, error)
}
