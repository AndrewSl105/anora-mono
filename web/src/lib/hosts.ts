import type { Agent, HostNode } from '@/api/types'
import { t } from '@/i18n/en'

/** Placement value for the server itself (POST /tasks `node`, project `node`). */
export const SERVER = 'server'

export type HostInfo = {
  /** '' for the server, else the node id. */
  id: string
  name: string
  online: boolean
  /** The node no longer exists (its tasks keep the id). */
  removed?: boolean
}

/** Display info for a task's or project's placement (null / '' / 'server' = the server). */
export function hostInfo(node: string | null | undefined, nodes: HostNode[] | undefined): HostInfo {
  if (!node || node === SERVER) return { id: '', name: t.hosts.server, online: true }
  const n = nodes?.find((x) => x.id === node)
  if (!n) return { id: node, name: nodes ? t.hosts.removed(node) : node, online: false, removed: !!nodes }
  return { id: n.id, name: n.name, online: n.online }
}

/**
 * Agents a new task may use on a host: the server's agents with keys, or the
 * agents a node reported when it last connected.
 */
export function hostAgents(node: string, nodes: HostNode[] | undefined, serverAgents: Agent[]): Agent[] {
  if (!node || node === SERVER) return serverAgents
  return (nodes?.find((n) => n.id === node)?.agents ?? []) as Agent[]
}

/** Columns of the canvas: the server first, then nodes in registration order, then removed ones. */
export function hostOrder(nodes: HostNode[] | undefined, taskNodes: Array<string | null>): string[] {
  const out = ['']
  for (const n of nodes ?? []) out.push(n.id)
  for (const id of taskNodes) if (id && !out.includes(id)) out.push(id)
  return out
}
