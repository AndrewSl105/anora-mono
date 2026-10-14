import { useEffect, useState } from 'react'
import { Bug, X } from 'lucide-react'
import { useRuntime } from './runtime'

/** Only rendered when VITE_DEBUG=1: platform, version, theme and insets. */
export default function DebugPanel() {
  const runtime = useRuntime()
  const [open, setOpen] = useState(false)
  const [, tick] = useState(0)
  useEffect(() => {
    if (!open) return
    const id = setInterval(() => tick((n) => n + 1), 1000)
    return () => clearInterval(id)
  }, [open])

  const tg = runtime.tg
  const css = getComputedStyle(document.documentElement)
  const rows: Array<[string, string]> = [
    ['mode', runtime.mode],
    ['platform', tg?.platform ?? '—'],
    ['version', tg?.version ?? '—'],
    ['scheme', tg?.colorScheme ?? '—'],
    ['viewport', tg ? `${tg.viewportHeight} / stable ${tg.viewportStableHeight}` : `${window.innerHeight}`],
    ['safeArea', tg?.safeAreaInset ? JSON.stringify(tg.safeAreaInset) : '—'],
    ['contentSafe', tg?.contentSafeAreaInset ? JSON.stringify(tg.contentSafeAreaInset) : '—'],
    ['--app-height', css.getPropertyValue('--app-height')],
    ['--safe-bottom', css.getPropertyValue('--safe-bottom')],
    ['start_param', tg?.initDataUnsafe?.start_param ?? '—'],
    ['theme', tg ? JSON.stringify(tg.themeParams) : '—'],
  ]

  return (
    <div className="fixed top-[calc(var(--safe-top)+4.5rem)] right-2 z-50 text-xs">
      {open ? (
        <div className="max-h-[60vh] w-72 overflow-auto rounded-lg border bg-popover p-3 font-mono shadow-lg">
          <button className="float-right" onClick={() => setOpen(false)} aria-label="Close debug panel">
            <X className="size-4" />
          </button>
          {rows.map(([k, v]) => (
            <div key={k} className="break-all">
              <span className="text-muted-foreground">{k}:</span> {v}
            </div>
          ))}
        </div>
      ) : (
        <button
          className="rounded-full border bg-popover p-2 shadow"
          onClick={() => setOpen(true)}
          aria-label="Open debug panel"
        >
          <Bug className="size-4" />
        </button>
      )}
    </div>
  )
}
