import { ShieldX, Clock, CloudOff } from 'lucide-react'
import type { ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import { useRuntime } from '@/telegram/runtime'
import { t } from '@/i18n/en'

function Screen({ icon, title, children }: { icon: ReactNode; title: string; children: ReactNode }) {
  return (
    <main className="flex min-h-(--app-height) flex-col items-center justify-center gap-3 px-6 text-center">
      <div className="mb-2 text-muted-foreground">{icon}</div>
      <h1 className="text-xl font-semibold">{title}</h1>
      {children}
    </main>
  )
}

export function DeniedScreen() {
  const runtime = useRuntime()
  const user = runtime.mode === 'telegram' ? runtime.tg.initDataUnsafe?.user : undefined
  const name = user ? [user.first_name, user.last_name].filter(Boolean).join(' ') || user.username || '' : ''
  return (
    <Screen icon={<ShieldX className="size-12" />} title={t.auth.deniedTitle}>
      <p className="max-w-xs text-muted-foreground">{t.auth.deniedBody}</p>
      {user && <p className="font-mono text-sm select-all">{t.auth.deniedUser(name, user.id)}</p>}
    </Screen>
  )
}

export function ExpiredScreen() {
  return (
    <Screen icon={<Clock className="size-12" />} title={t.auth.expiredTitle}>
      <p className="max-w-xs text-muted-foreground">{t.auth.expiredBody}</p>
    </Screen>
  )
}

export function UnavailableScreen({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <Screen icon={<CloudOff className="size-12" />} title={t.auth.unavailableTitle}>
      <p className="max-w-xs text-muted-foreground">{message}</p>
      <Button className="mt-2 h-11" onClick={onRetry}>
        {t.common.retry}
      </Button>
    </Screen>
  )
}
