import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { setupServer } from 'msw/node'
import { http, HttpResponse } from 'msw'
import { RuntimeProvider, type Runtime } from '@/telegram/runtime'
import type { WebApp } from '@/telegram/webapp'
import { api, call, setBearerToken } from '@/api/client'
import { AuthGate, initDataExpired, useSession } from './AuthGate'
import { loginErrorMessage } from './LoginScreen'
import { ApiError } from '@/api/errors'

const server = setupServer()
beforeAll(() => server.listen({ onUnhandledFrame: 'error' }))
afterEach(() => {
  server.resetHandlers()
  setBearerToken(null)
})
afterAll(() => server.close())

const json = (body: unknown, status = 200) => HttpResponse.json(body as Record<string, unknown>, { status })
const unauthorized = () => json({ error: { code: 'unauthorized', message: 'no' } }, 401)

function App() {
  const s = useSession()
  return <p>app ready ({s.session})</p>
}

function renderGate(runtime: Runtime) {
  return render(
    <RuntimeProvider runtime={runtime}>
      <QueryClientProvider client={new QueryClient()}>
        <AuthGate>
          <App />
        </AuthGate>
      </QueryClientProvider>
    </RuntimeProvider>,
  )
}

function telegram(initData = 'query_id=1&hash=h', authDate = Math.floor(Date.now() / 1000)): Runtime {
  const noop = () => {}
  const tg = {
    initData,
    initDataUnsafe: { auth_date: authDate, user: { id: 77, first_name: 'Ann' } },
    version: '8.0',
    isVersionAtLeast: () => true,
    ready: noop,
    expand: noop,
    onEvent: noop,
    offEvent: noop,
    setHeaderColor: noop,
    setBackgroundColor: noop,
    setBottomBarColor: noop,
    themeParams: {},
    colorScheme: 'light',
    viewportStableHeight: 700,
  } as unknown as WebApp
  return { mode: 'telegram', tg }
}

describe('AuthGate in a browser', () => {
  it('shows the app when the session is valid', async () => {
    server.use(http.get('*/api/v1/me', () => json({ ok: true, kind: 'web' })))
    renderGate({ mode: 'browser' })
    expect(await screen.findByText('app ready (web)')).toBeInTheDocument()
  })

  it('logs in with password and TOTP, auto-submitting on the 6th digit', async () => {
    let body: unknown
    let csrf: string | null = null
    server.use(
      http.get('*/api/v1/me', unauthorized),
      http.post('*/api/v1/auth/login', async ({ request }) => {
        body = await request.json()
        csrf = request.headers.get('X-Requested-With')
        return json({ ok: true, kind: 'web' })
      }),
    )
    renderGate({ mode: 'browser' })
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Password'), 'secret')
    await user.type(screen.getByLabelText('Authenticator code'), '12a3456')
    expect(await screen.findByText('app ready (web)')).toBeInTheDocument()
    expect(body).toEqual({ password: 'secret', totp: '123456' })
    expect(csrf).toBe('anora')
  })

  it('shows one generic message on bad credentials', async () => {
    server.use(
      http.get('*/api/v1/me', unauthorized),
      http.post('*/api/v1/auth/login', () => json({ error: { code: 'bad_credentials', message: 'x' } }, 401)),
    )
    renderGate({ mode: 'browser' })
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Password'), 'bad')
    await user.type(screen.getByLabelText('Authenticator code'), '000000')
    expect(await screen.findByRole('alert')).toHaveTextContent('Wrong password or code.')
  })

  it('returns to the login screen when a call gets 401 mid-session', async () => {
    server.use(
      http.get('*/api/v1/me', () => json({ ok: true, kind: 'web' })),
      http.get('*/api/v1/stats', unauthorized),
    )
    renderGate({ mode: 'browser' })
    await screen.findByText('app ready (web)')
    await expect(call(api.GET('/api/v1/stats'))).rejects.toMatchObject({ status: 401 })
    expect(await screen.findByLabelText('Password')).toBeInTheDocument()
  })
})

describe('AuthGate in Telegram', () => {
  it('logs in automatically with the raw initData', async () => {
    let sent: unknown
    server.use(
      http.post('*/api/v1/auth/telegram', async ({ request }) => {
        sent = await request.json()
        return json({ ok: true, kind: 'telegram', token: 't1' })
      }),
    )
    renderGate(telegram('query_id=1&user=%7B%7D&hash=abc'))
    expect(await screen.findByText('app ready (telegram)')).toBeInTheDocument()
    expect(sent).toEqual({ init_data: 'query_id=1&user=%7B%7D&hash=abc' })
  })

  it('shows Access denied with the Telegram user on 401/403', async () => {
    server.use(http.post('*/api/v1/auth/telegram', unauthorized))
    renderGate(telegram())
    expect(await screen.findByText('Access denied')).toBeInTheDocument()
    expect(screen.getByText('Ann · id 77')).toBeInTheDocument()
  })

  it('asks to reopen when initData is older than an hour', async () => {
    server.use(http.post('*/api/v1/auth/telegram', unauthorized))
    renderGate(telegram('x', Math.floor(Date.now() / 1000) - 7200))
    expect(await screen.findByText('Session expired')).toBeInTheDocument()
  })

  it('re-authenticates once on a mid-session 401 and retries the call', async () => {
    let logins = 0
    let stats = 0
    server.use(
      http.post('*/api/v1/auth/telegram', () => {
        logins++
        return json({ ok: true, kind: 'telegram', token: `t${logins}` })
      }),
      http.get('*/api/v1/stats', () => (++stats === 1 ? unauthorized() : json({ running: 1 }))),
    )
    renderGate(telegram())
    await screen.findByText('app ready (telegram)')
    await expect(call(api.GET('/api/v1/stats'))).resolves.toMatchObject({ running: 1 })
    await waitFor(() => expect(logins).toBe(2))
    expect(stats).toBe(2)
  })

  // Telegram Web loads the Mini App in a cross-site iframe without the cookie.
  it('sends the session token as a bearer token and retries with the new one after re-auth', async () => {
    let logins = 0
    const seen: (string | null)[] = []
    server.use(
      http.post('*/api/v1/auth/telegram', () => {
        logins++
        return json({ ok: true, kind: 'telegram', token: `tok${logins}` })
      }),
      http.get('*/api/v1/stats', ({ request }) => {
        seen.push(request.headers.get('Authorization'))
        return seen.length === 1 ? unauthorized() : json({ running: 0 })
      }),
    )
    renderGate(telegram())
    await screen.findByText('app ready (telegram)')
    await call(api.GET('/api/v1/stats'))
    expect(seen).toEqual(['Bearer tok1', 'Bearer tok2'])
  })
})

describe('helpers', () => {
  it('formats 429 with Retry-After or the 15-minute window', () => {
    expect(loginErrorMessage(new ApiError(429, 'rate_limited', '', 120))).toBe('Too many attempts, try again in 2 min.')
    expect(loginErrorMessage(new ApiError(429, 'rate_limited', ''))).toBe('Too many attempts, try again in 15 min.')
  })

  it('detects stale initData', () => {
    expect(initDataExpired(telegram('x', 1000), 1000 * 1000 + 3601 * 1000)).toBe(true)
    expect(initDataExpired({ mode: 'browser' })).toBe(false)
  })
})
