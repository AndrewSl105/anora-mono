import { Link } from 'react-router'
import { Button } from '@/components/ui/button'
import { t } from '@/i18n/en'

export function NotFound({ title = t.system.notFound }: { title?: string }) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-4 px-6 text-center">
      <p className="text-5xl font-semibold text-muted-foreground/50">404</p>
      <h1 className="text-lg font-medium">{title}</h1>
      <Button asChild className="h-11">
        <Link to="/">{t.system.goHome}</Link>
      </Button>
    </div>
  )
}
