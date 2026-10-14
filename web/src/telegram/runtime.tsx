import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
import { detectMode, getWebApp, supports, type WebApp } from './webapp'
import { applyTelegramTheme } from './theme'
import { applyViewport } from './viewport'

export type Runtime = { mode: 'telegram'; tg: WebApp } | { mode: 'browser'; tg?: undefined }

export function resolveRuntime(tg: WebApp | undefined = getWebApp()): Runtime {
  return detectMode(tg) === 'telegram' ? { mode: 'telegram', tg: tg! } : { mode: 'browser' }
}

const RuntimeContext = createContext<Runtime>({ mode: 'browser' })

export function RuntimeProvider({ runtime, children }: { runtime: Runtime; children: ReactNode }) {
  useEffect(() => {
    if (runtime.mode !== 'telegram') return
    const tg = runtime.tg
    const theme = () => applyTelegramTheme(tg)
    const viewport = () => applyViewport(tg)
    theme()
    viewport()
    tg.ready()
    tg.expand()
    tg.onEvent('themeChanged', theme)
    tg.onEvent('viewportChanged', viewport)
    if (supports(tg, 'safeArea')) {
      tg.onEvent('safeAreaChanged', viewport)
      tg.onEvent('contentSafeAreaChanged', viewport)
    }
    return () => {
      tg.offEvent('themeChanged', theme)
      tg.offEvent('viewportChanged', viewport)
      if (supports(tg, 'safeArea')) {
        tg.offEvent('safeAreaChanged', viewport)
        tg.offEvent('contentSafeAreaChanged', viewport)
      }
    }
  }, [runtime])
  return <RuntimeContext.Provider value={runtime}>{children}</RuntimeContext.Provider>
}

/** Components use this instead of touching window.Telegram. */
export function useRuntime(): Runtime {
  return useContext(RuntimeContext)
}

/** True on screens ≥ 1024 px (sidebar + list + detail). */
export function useIsDesktop(): boolean {
  const query = '(min-width: 1024px)'
  const [desktop, setDesktop] = useState(() => typeof window !== 'undefined' && window.matchMedia?.(query).matches)
  useEffect(() => {
    const mq = window.matchMedia?.(query)
    if (!mq) return
    const onChange = () => setDesktop(mq.matches)
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [])
  return desktop
}
