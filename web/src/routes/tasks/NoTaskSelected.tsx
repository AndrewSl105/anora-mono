import { MousePointerClick } from 'lucide-react'
import { t } from '@/i18n/en'

export function NoTaskSelected() {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center text-muted-foreground">
      <MousePointerClick className="size-8" aria-hidden="true" />
      <p className="text-sm">{t.tasks.selectTask}</p>
    </div>
  )
}
