import type { Agent } from '@/api/types'

/** Every agent, in the order the server lists them (docs/openapi.yaml, Agent). */
export const AGENTS: readonly Agent[] = ['claude', 'codex', 'cursor']
