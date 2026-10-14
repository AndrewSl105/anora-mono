import { Logo } from '@/components/Logo'
import { t } from '@/i18n/en'

export function Splash() {
  return (
    <div className="flex h-(--app-height) flex-col items-center justify-center gap-5" role="status" aria-label={t.auth.signingIn}>
      <Logo className="size-14" />
      <div className="size-6 animate-spin rounded-full border-2 border-muted-foreground/25 border-t-primary" />
    </div>
  )
}
