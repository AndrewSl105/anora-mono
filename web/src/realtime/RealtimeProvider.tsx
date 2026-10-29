import { createContext, useContext, useEffect, useState, useSyncExternalStore, type ReactNode } from 'react'
import { useQueryClient, type QueryClient } from '@tanstack/react-query'
import { RealtimeClient, wsUrl, type ServerMessage, type SocketStatus } from './socket'
import { appendEvents, upsertTask } from '@/api/cache'
import { api, call, CSRF, hasBearerToken } from '@/api/client'
import { keys } from '@/api/keys'
import { upsertNode } from '@/api/hooks'

const RealtimeContext = createContext<RealtimeClient | null>(null)

export function handleMessage(qc: QueryClient, msg: ServerMessage) {
  if (msg.op === 'event') {
    appendEvents(qc, msg.task_id, [msg.event])
    if (msg.event.type === 'run_started' || msg.event.type === 'run_finished') {
      void qc.invalidateQueries({ queryKey: keys.tasks.runs(msg.task_id), exact: true })
    }
  } else if (msg.op === 'task') upsertTask(qc, msg.task)
  else if (msg.op === 'node') upsertNode(qc, msg.node)
}

/**
 * Cookie sessions connect directly. Bearer (Telegram) sessions cannot send a
 * header on the WebSocket handshake, so each connect uses a fresh single-use ticket.
 */
export async function socketUrl(): Promise<string> {
  if (!hasBearerToken()) return wsUrl()
  const { ticket } = await call(api.POST('/api/v1/ws/ticket', { params: { header: CSRF } }))
  return `${wsUrl()}?ticket=${encodeURIComponent(ticket)}`
}

/** After every (re)connect: fetch events missed while offline, refresh lists. */
export function catchUp(qc: QueryClient, taskIds: number[]) {
  for (const id of taskIds) {
    void qc.refetchQueries({ queryKey: keys.tasks.events(id), exact: true })
    void qc.invalidateQueries({ queryKey: keys.tasks.detail(id), exact: true })
  }
  void qc.invalidateQueries({ queryKey: keys.tasks.lists() })
  void qc.invalidateQueries({ queryKey: keys.nodes })
}

export function RealtimeProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient()
  const [client] = useState(
    () =>
      new RealtimeClient({
        url: socketUrl,
        onMessage: (msg) => handleMessage(qc, msg),
        onOpen: (taskIds) => catchUp(qc, taskIds),
      }),
  )

  useEffect(() => {
    client.start()
    const onVisibility = () => client.setHidden(document.visibilityState === 'hidden')
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      document.removeEventListener('visibilitychange', onVisibility)
      client.stop()
    }
  }, [client])

  return <RealtimeContext.Provider value={client}>{children}</RealtimeContext.Provider>
}

function useClient() {
  return useContext(RealtimeContext)
}

export function useTasksSubscription() {
  const client = useClient()
  useEffect(() => client?.subscribe({ scope: 'tasks' }), [client])
}

export function useTaskSubscription(taskId: number) {
  const client = useClient()
  useEffect(() => client?.subscribe({ task_id: taskId }), [client, taskId])
}

const noop = () => () => {}
export function useSocketStatus(): SocketStatus {
  const client = useClient()
  return useSyncExternalStore(client?.onStatus ?? noop, client?.getStatus ?? (() => 'idle' as const))
}
