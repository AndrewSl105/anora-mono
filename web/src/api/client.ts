import createClient from 'openapi-fetch'
import type { paths } from './schema'
import { toApiError } from './errors'

/** The backend rejects state-changing calls without this header (403 csrf). */
export const CSRF = { 'X-Requested-With': 'anora' } as const

/**
 * Telegram sessions: the session token from POST /auth/telegram, kept in
 * memory only (never in storage). Telegram Web embeds the Mini App in a
 * cross-site iframe where the SameSite=Lax session cookie is not sent, so
 * Telegram mode authenticates with `Authorization: Bearer` instead.
 */
let bearer: string | null = null

export function setBearerToken(token: string | null) {
  bearer = token
}

export function hasBearerToken(): boolean {
  return bearer !== null
}

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
  // One re-auth for all concurrent 401s, then one retry per request (with the
  // new bearer token, if re-auth issued one).
  reauth ??= onUnauthorized().finally(() => {
    reauth = null
  })
  if (!(await reauth)) return response
  if (bearer) retry.headers.set('Authorization', `Bearer ${bearer}`)
  return fetch(retry)
}

export const api = createClient<paths>({
  baseUrl: typeof window === 'undefined' ? '' : window.location.origin,
  credentials: 'include',
  fetch: authFetch,
})

api.use({
  onRequest({ request }) {
    if (request.method !== 'GET' && request.method !== 'HEAD') request.headers.set('X-Requested-With', 'anora')
    if (bearer) request.headers.set('Authorization', `Bearer ${bearer}`)
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

export type UploadProgress = (fraction: number) => void

/**
 * POSTs multipart/form-data with upload progress (fetch has none, so this is
 * XHR). Same rules as `api`: CSRF header, the Telegram bearer token, cookies,
 * and one re-auth + retry after a 401. Resolves with the JSON body on 2xx,
 * rejects with an ApiError otherwise.
 */
export async function uploadForm<T>(path: string, form: FormData, onProgress?: UploadProgress): Promise<T> {
  let res = await sendForm(path, form, onProgress)
  if (res.status === 401 && onUnauthorized && !NO_REAUTH.some((p) => path.startsWith(p))) {
    reauth ??= onUnauthorized().finally(() => {
      reauth = null
    })
    if (await reauth) res = await sendForm(path, form, onProgress)
  }
  const body: unknown = res.body ? safeJson(res.body) : undefined
  if (res.status >= 200 && res.status < 300) return body as T
  throw await toApiError(new Response(null, { status: res.status, statusText: res.statusText, headers: res.headers }), body)
}

type FormResult = { status: number; statusText: string; body: string; headers: Headers }

function sendForm(path: string, form: FormData, onProgress?: UploadProgress): Promise<FormResult> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('POST', path)
    xhr.withCredentials = true
    xhr.setRequestHeader('X-Requested-With', 'anora')
    if (bearer) xhr.setRequestHeader('Authorization', `Bearer ${bearer}`)
    if (onProgress) {
      xhr.upload.onprogress = (e) => {
        if (e.lengthComputable && e.total > 0) onProgress(e.loaded / e.total)
      }
    }
    xhr.onload = () => {
      const headers = new Headers()
      const retryAfter = xhr.getResponseHeader('Retry-After')
      if (retryAfter) headers.set('Retry-After', retryAfter)
      resolve({ status: xhr.status, statusText: xhr.statusText, body: xhr.responseText, headers })
    }
    xhr.onerror = () => reject(new TypeError('Network error while uploading'))
    xhr.onabort = () => reject(new DOMException('Upload aborted', 'AbortError'))
    xhr.send(form)
  })
}

function safeJson(text: string): unknown {
  try {
    return JSON.parse(text)
  } catch {
    return undefined
  }
}
