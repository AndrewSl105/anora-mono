import { Component, type ErrorInfo, type ReactNode } from 'react'
import { TriangleAlert } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { t } from '@/i18n/en'

type State = { error: Error | null }

export class ErrorBoundary extends Component<{ children: ReactNode }, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error(error, info.componentStack)
  }

  render() {
    if (!this.state.error) return this.props.children
    return <CrashScreen error={this.state.error} />
  }
}

export function CrashScreen({ error }: { error?: unknown }) {
  return (
    <main className="flex min-h-(--app-height) flex-col items-center justify-center gap-3 px-6 text-center">
      <TriangleAlert className="size-12 text-destructive" aria-hidden="true" />
      <h1 className="text-xl font-semibold">{t.system.crashed}</h1>
      {error instanceof Error && <p className="max-w-sm font-mono text-xs break-all text-muted-foreground">{error.message}</p>}
      <Button className="mt-2 h-11" onClick={() => window.location.reload()}>
        {t.common.reload}
      </Button>
    </main>
  )
}
