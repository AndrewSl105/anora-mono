import { useEffect, useState } from 'react'
import { WifiOff } from 'lucide-react'
import { useSocketStatus } from '@/realtime/RealtimeProvider'
import { t } from '@/i18n/en'

function useOnline() {
  const [online, setOnline] = useState(() => navigator.onLine)
  useEffect(() => {
    const on = () => setOnline(true)
    const off = () => setOnline(false)
    window.addEventListener('online', on)
    window.addEventListener('offline', off)
    return () => {
      window.removeEventListener('online', on)
      window.removeEventListener('offline', off)
    }
  }, [])
  return online
}

export function OfflineBanner() {
  const status = useSocketStatus()
  const online = useOnline()
  if (online && status !== 'closed') return null
  return (
    <div role="status" className="flex items-center justify-center gap-2 bg-amber-500/15 px-4 py-1.5 text-xs font-medium text-amber-800 dark:text-amber-200">
      <WifiOff className="size-3.5" aria-hidden="true" />
      {t.system.offline}
    </div>
  )
}
