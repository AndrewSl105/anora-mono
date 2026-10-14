import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { storage } from './storage'
import { useRuntime } from '@/telegram/runtime'

export type ThemeChoice = 'system' | 'light' | 'dark'
const KEY = 'anora.theme'

type ThemeState = { choice: ThemeChoice; resolved: 'light' | 'dark'; setChoice: (c: ThemeChoice) => void }

const ThemeContext = createContext<ThemeState>({ choice: 'system', resolved: 'light', setChoice: () => {} })

function systemDark() {
  return typeof window !== 'undefined' && !!window.matchMedia?.('(prefers-color-scheme: dark)').matches
}

/** Browser-mode light/dark. In Telegram mode the colours follow themeParams instead. */
export function ThemeProvider({ children }: { children: ReactNode }) {
  const runtime = useRuntime()
  const [choice, setChoiceState] = useState<ThemeChoice>(() => (storage.get(KEY) as ThemeChoice | null) ?? 'system')
  const [sysDark, setSysDark] = useState(systemDark)

  useEffect(() => {
    const mq = window.matchMedia?.('(prefers-color-scheme: dark)')
    if (!mq) return
    const onChange = () => setSysDark(mq.matches)
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [])

  const telegramDark = runtime.mode === 'telegram' && runtime.tg.colorScheme === 'dark'
  const resolved: 'light' | 'dark' =
    runtime.mode === 'telegram' ? (telegramDark ? 'dark' : 'light') : choice === 'system' ? (sysDark ? 'dark' : 'light') : choice

  useEffect(() => {
    if (runtime.mode === 'telegram') return
    document.documentElement.classList.toggle('dark', resolved === 'dark')
    document.documentElement.style.colorScheme = resolved
  }, [resolved, runtime.mode])

  const value = useMemo<ThemeState>(
    () => ({
      choice,
      resolved,
      setChoice: (c) => {
        storage.set(KEY, c)
        setChoiceState(c)
      },
    }),
    [choice, resolved],
  )
  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>
}

export function useTheme() {
  return useContext(ThemeContext)
}
