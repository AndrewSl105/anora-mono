import type { ReactNode } from 'react'
import { LogOut, Monitor, Moon, Sun } from 'lucide-react'
import { useHealth, useStats } from '@/api/hooks'
import { useSession } from '@/auth/AuthGate'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useTheme, type ThemeChoice } from '@/lib/theme'
import { usd } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useRuntime } from '@/telegram/runtime'
import { t } from '@/i18n/en'

const s = t.settings

function Stat({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="rounded-xl border bg-card px-3.5 py-3">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="mt-0.5 text-xl font-semibold tabular-nums">{value}</div>
    </div>
  )
}

const THEMES: Array<{ value: ThemeChoice; icon: typeof Sun }> = [
  { value: 'system', icon: Monitor },
  { value: 'light', icon: Sun },
  { value: 'dark', icon: Moon },
]

export default function SettingsPage() {
  const runtime = useRuntime()
  const session = useSession()
  const stats = useStats()
  const health = useHealth()
  const theme = useTheme()

  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto max-w-2xl space-y-8 px-4 pt-4 pb-8">
        <h1 className="text-xl font-semibold">{s.title}</h1>

        <section>
          <h2 className="mb-3 text-sm font-semibold tracking-wide text-muted-foreground uppercase">{s.stats}</h2>
          {stats.data ? (
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
              <Stat label={s.running} value={stats.data.running} />
              <Stat label={s.queued} value={stats.data.queued} />
              <Stat label={s.waiting} value={stats.data.waiting_input} />
              <Stat label={s.costToday} value={usd(stats.data.cost_usd_today)} />
              <Stat label={s.costMonth} value={usd(stats.data.cost_usd_month)} />
            </div>
          ) : (
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
              {[0, 1, 2, 3, 4].map((i) => (
                <Skeleton key={i} className="h-[68px] rounded-xl" />
              ))}
            </div>
          )}
        </section>

        {runtime.mode === 'browser' && (
          <section>
            <h2 className="mb-3 text-sm font-semibold tracking-wide text-muted-foreground uppercase">{s.theme}</h2>
            <div role="radiogroup" aria-label={s.theme} className="grid grid-cols-3 gap-2">
              {THEMES.map(({ value, icon: Icon }) => (
                <button
                  key={value}
                  role="radio"
                  aria-checked={theme.choice === value}
                  onClick={() => theme.setChoice(value)}
                  className={cn(
                    'flex h-11 items-center justify-center gap-2 rounded-lg border text-sm font-medium',
                    theme.choice === value && 'border-primary bg-primary/10 text-primary',
                  )}
                >
                  <Icon className="size-4" aria-hidden="true" />
                  {s.themes[value]}
                </button>
              ))}
            </div>
          </section>
        )}

        <section>
          <h2 className="mb-3 text-sm font-semibold tracking-wide text-muted-foreground uppercase">{s.about}</h2>
          <dl className="divide-y rounded-xl border px-3.5 text-sm">
            <div className="flex justify-between py-2.5">
              <dt className="text-muted-foreground">{s.version}</dt>
              <dd className="font-mono">{import.meta.env.VITE_APP_VERSION || 'dev'}</dd>
            </div>
            <div className="flex justify-between gap-4 py-2.5">
              <dt className="text-muted-foreground">{s.backend}</dt>
              <dd className="text-right">
                {health.data ? (
                  <span className={health.data.ok ? 'text-emerald-700 dark:text-emerald-300' : 'text-red-700 dark:text-red-300'}>
                    {health.data.ok ? s.healthy : s.unhealthy}
                    <span className="block font-mono text-xs text-muted-foreground">
                      {Object.entries(health.data.checks)
                        .map(([k, v]) => `${k}: ${v}`)
                        .join(' · ')}
                    </span>
                  </span>
                ) : (
                  '…'
                )}
              </dd>
            </div>
            <div className="flex justify-between py-2.5">
              <dt className="text-muted-foreground">Session</dt>
              <dd>{session.session}</dd>
            </div>
          </dl>
        </section>

        {runtime.mode === 'browser' && (
          <Button variant="outline" className="h-11 w-full" onClick={() => void session.logout()}>
            <LogOut /> {t.auth.logout}
          </Button>
        )}
      </div>
    </div>
  )
}
