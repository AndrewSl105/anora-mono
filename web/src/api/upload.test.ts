import { afterEach, describe, expect, it, vi } from 'vitest'
import { setBearerToken, setUnauthorizedHandler, uploadForm } from './client'
import { ApiError } from './errors'

type Reply = { status: number; body: unknown }

/** A fake XMLHttpRequest that answers each send with the next reply. */
function fakeXhr(replies: Reply[]) {
  const sent: Array<{ url: string; headers: Record<string, string>; body: unknown; credentials: boolean }> = []
  class FakeXHR {
    status = 0
    statusText = ''
    responseText = ''
    withCredentials = false
    upload: { onprogress: ((e: ProgressEvent) => void) | null } = { onprogress: null }
    onload: (() => void) | null = null
    onerror: (() => void) | null = null
    onabort: (() => void) | null = null
    private url = ''
    private headers: Record<string, string> = {}
    open(_method: string, url: string) {
      this.url = url
    }
    setRequestHeader(k: string, v: string) {
      this.headers[k] = v
    }
    getResponseHeader() {
      return null
    }
    send(body: unknown) {
      sent.push({ url: this.url, headers: this.headers, body, credentials: this.withCredentials })
      const r = replies.shift()!
      this.upload.onprogress?.({ lengthComputable: true, loaded: 5, total: 10 } as ProgressEvent)
      this.status = r.status
      this.responseText = JSON.stringify(r.body)
      queueMicrotask(() => this.onload?.())
    }
  }
  vi.stubGlobal('XMLHttpRequest', FakeXHR)
  return sent
}

afterEach(() => {
  vi.unstubAllGlobals()
  setBearerToken(null)
  setUnauthorizedHandler(null)
})

describe('uploadForm', () => {
  it('sends the CSRF header, the bearer token and cookies, and reports progress', async () => {
    const sent = fakeXhr([{ status: 201, body: { id: 1 } }])
    setBearerToken('tok')
    const progress: number[] = []
    const form = new FormData()
    await expect(uploadForm('/api/v1/tasks', form, (f) => progress.push(f))).resolves.toEqual({ id: 1 })
    expect(sent[0]).toMatchObject({ url: '/api/v1/tasks', credentials: true, body: form })
    expect(sent[0].headers).toEqual({ 'X-Requested-With': 'anora', Authorization: 'Bearer tok' })
    expect(progress).toEqual([0.5])
  })

  it('re-authenticates once after a 401 and retries with the new token', async () => {
    const sent = fakeXhr([
      { status: 401, body: { error: { code: 'unauthorized', message: 'no' } } },
      { status: 200, body: { ok: true } },
    ])
    setUnauthorizedHandler(async () => {
      setBearerToken('fresh')
      return true
    })
    await expect(uploadForm('/api/v1/tasks/1/message', new FormData())).resolves.toEqual({ ok: true })
    expect(sent.map((s) => s.headers.Authorization)).toEqual([undefined, 'Bearer fresh'])
  })

  it('turns an error body into an ApiError', async () => {
    fakeXhr([{ status: 400, body: { error: { code: 'invalid', message: '"x.pdf" is not a PNG, JPEG, WebP or GIF image' } } }])
    const err = await uploadForm('/api/v1/tasks', new FormData()).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err).toMatchObject({ status: 400, code: 'invalid', message: '"x.pdf" is not a PNG, JPEG, WebP or GIF image' })
  })
})
