import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { api, call, CSRF, setUnauthorizedHandler } from '@/api/client'
import { isApiError } from '@/api/errors'
import { useRuntime, type Runtime } from '@/telegram/runtime'
import { t } from '@/i18n/en'
import { Splash } from './Splash'
import { LoginScreen } from './LoginScreen'
import { DeniedScreen, ExpiredScreen, UnavailableScreen } from './DeniedScreen'

export type AuthState =
  | { kind: 'checking' }
  | { kind: 'login' }
  | { kind: 'denied' }
  | { kind: 'expired' }
  | { kind: 'unavailable'; message: string }
  | { kind: 'ready'; session: 'web' | 'telegram' }

const INIT_DATA_MAX_AGE_S = 3600

export function initDataExpired(runtime: Runtime, now = Date.now()): boolean {
  if (runtime.mode !== 'telegram') return false
  const authDate = runtime.tg.initDataUnsafe?.auth_date
  return !!authDate && now / 1000 - authDate > INIT_DATA_MAX_AGE_S
}

async function telegramLogin(initData: string) {
  return call(api.POST('/api/v1/auth/telegram', { params: { header: CSRF }, body: { init_data: initData } }))
}

/** Telegram: automatic login with initData. Browser: session check, then password + TOTP. */
export async function resolveAuth(runtime: Runtime): Promise<AuthState> {
  try {
    if (runtime.mode === 'telegram') {
      const s = await telegramLogin(runtime.tg.initData)
      return { kind: 'ready', session: s.kind }
    }
    const s = await call(api.GET('/api/v1/me'))
    return { kind: 'ready', session: s.kind }
  } catch (e) {
    if (runtime.mode === 'browser' && isApiError(e) && e.status === 401) return { kind: 'login' }
    if (runtime.mode === 'telegram' && isApiError(e) && (e.status === 401 || e.status === 403)) {
      return initDataExpired(runtime) ? { kind: 'expired' } : { kind: 'denied' }
    }
    return { kind: 'unavailable', message: isApiError(e) ? e.message : t.auth.unreachable }
  }
}

type Session = { session: 'web' | 'telegram'; logout: () => Promise<void> }
const SessionContext = createContext<Session | null>(null)

export function useSession(): Session {
  const s = useContext(SessionContext)
  if (!s) throw new Error('useSession outside AuthGate')
  return s
}

export function AuthGate({ children }: { children: ReactNode }) {
  const runtime = useRuntime()
  const qc = useQueryClient()
  const [state, setState] = useState<AuthState>({ kind: 'checking' })
  const attempt = useRef(0)

  const run = useCallback(() => {
    const n = ++attempt.current
    void resolveAuth(runtime).then((s) => {
      if (n === attempt.current) setState(s)
    })
  }, [runtime])

  useEffect(() => {
    run()
  }, [run])

  const check = useCallback(() => {
    setState({ kind: 'checking' })
    run()
  }, [run])

  // A 401 from any call: Telegram repeats the login once, browser goes back to the login screen.
  useEffect(() => {
    setUnauthorizedHandler(async () => {
      if (runtime.mode === 'telegram') {
        try {
          await telegramLogin(runtime.tg.initData)
          return true
        } catch {
          setState(initDataExpired(runtime) ? { kind: 'expired' } : { kind: 'denied' })
          return false
        }
      }
      qc.clear()
      setState({ kind: 'login' })
      return false
    })
    return () => setUnauthorizedHandler(null)
  }, [runtime, qc])

  const logout = useCallback(async () => {
    try {
      await call(api.POST('/api/v1/auth/logout', { params: { header: CSRF } }))
    } finally {
      qc.clear()
      setState({ kind: 'login' })
    }
  }, [qc])

  const session = useMemo<Session | null>(
    () => (state.kind === 'ready' ? { session: state.session, logout } : null),
    [state, logout],
  )

  switch (state.kind) {
    case 'checking':
      return <Splash />
    case 'login':
      return <LoginScreen onSuccess={(s) => setState({ kind: 'ready', session: s })} />
    case 'denied':
      return <DeniedScreen />
    case 'expired':
      return <ExpiredScreen />
    case 'unavailable':
      return <UnavailableScreen message={state.message} onRetry={check} />
    case 'ready':
      return <SessionContext.Provider value={session}>{children}</SessionContext.Provider>
  }
}
