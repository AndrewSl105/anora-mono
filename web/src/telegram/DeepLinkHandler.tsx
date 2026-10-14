import { useEffect } from 'react'
import { useNavigate } from 'react-router'
import { useRuntime } from './runtime'
import { parseDeepLink } from './deepLink'

let consumed = false

/** After auth, open the task a bot button pointed at (once per app start). */
export function DeepLink() {
  const runtime = useRuntime()
  const navigate = useNavigate()
  useEffect(() => {
    if (consumed) return
    consumed = true
    const startParam = runtime.mode === 'telegram' ? runtime.tg.initDataUnsafe?.start_param : undefined
    const id = parseDeepLink(startParam, window.location.search)
    if (id !== null) void navigate(`/tasks/${id}`, { replace: true })
  }, [runtime, navigate])
  return null
}
