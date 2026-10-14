const TASK_RE = /^task_(\d+)$/
const ID_RE = /^\d+$/

/**
 * Task id from a deep link: `start_param` "task_<id>" (t.me/<bot>/<app>?startapp=task_123)
 * or a `?task=<id>` query parameter on the app URL (web_app inline buttons).
 */
export function parseDeepLink(startParam: string | undefined | null, search: string): number | null {
  const fromStart = startParam ? TASK_RE.exec(startParam.trim()) : null
  if (fromStart) return Number(fromStart[1])
  const fromQuery = new URLSearchParams(search).get('task')
  if (fromQuery && ID_RE.test(fromQuery.trim())) return Number(fromQuery.trim())
  return null
}
