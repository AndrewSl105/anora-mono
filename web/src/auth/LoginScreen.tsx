import { useState, type FormEvent } from 'react'
import { api, call, CSRF } from '@/api/client'
import { isApiError } from '@/api/errors'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Logo } from '@/components/Logo'
import { t } from '@/i18n/en'

/** Fallback when 429 has no Retry-After: the backend window from docs/backend/API.md. */
const RATE_LIMIT_WINDOW_MIN = 15

export function loginErrorMessage(e: unknown): string {
  if (isApiError(e) && e.status === 429) {
    const minutes = e.retryAfter !== undefined ? Math.max(1, Math.ceil(e.retryAfter / 60)) : RATE_LIMIT_WINDOW_MIN
    return t.auth.tooMany(minutes)
  }
  if (isApiError(e) && (e.status === 401 || e.status === 400)) return t.auth.badCredentials
  return t.common.somethingWrong
}

export function LoginScreen({ onSuccess }: { onSuccess: (session: 'web' | 'telegram') => void }) {
  const [password, setPassword] = useState('')
  const [totp, setTotp] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(code = totp) {
    if (busy || !password || code.length !== 6) return
    setBusy(true)
    setError(null)
    try {
      const s = await call(api.POST('/api/v1/auth/login', { params: { header: CSRF }, body: { password, totp: code } }))
      onSuccess(s.kind)
    } catch (e) {
      setError(loginErrorMessage(e))
      setTotp('')
    } finally {
      setBusy(false)
    }
  }

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    void submit()
  }

  function onCode(value: string) {
    const digits = value.replace(/\D/g, '').slice(0, 6)
    setTotp(digits)
    if (digits.length === 6 && password) void submit(digits)
  }

  return (
    <main className="flex min-h-(--app-height) items-center justify-center px-4 py-10">
      <form onSubmit={onSubmit} className="w-full max-w-sm space-y-6" noValidate>
        <div className="flex flex-col items-center gap-3 text-center">
          <Logo className="size-12" />
          <h1 className="text-xl font-semibold">{t.auth.title}</h1>
        </div>
        <div className="space-y-2">
          <Label htmlFor="password">{t.auth.password}</Label>
          <Input
            id="password"
            type="password"
            autoComplete="current-password"
            autoFocus
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className="h-11"
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="totp">{t.auth.code}</Label>
          <Input
            id="totp"
            inputMode="numeric"
            autoComplete="one-time-code"
            pattern="[0-9]*"
            maxLength={6}
            value={totp}
            onChange={(e) => onCode(e.target.value)}
            className="h-11 font-mono tracking-[0.4em]"
          />
        </div>
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}
        <Button type="submit" className="h-11 w-full" disabled={busy || !password || totp.length !== 6}>
          {busy ? t.auth.signingIn : t.auth.signIn}
        </Button>
      </form>
    </main>
  )
}
