import { describe, expect, it } from 'vitest'
import type { Project } from '@/api/types'
import { createBody, patchBody, toFormValues } from './projectForm'

const project: Project = {
  id: 'vatary',
  name: 'Vatary',
  instructions: 'x',
  services: ['postgres'],
  env: { NODE_ENV: 'test' },
  runner_image: null,
  model: 'claude-opus-5-5',
  max_turns: 80,
  max_budget_usd: null,
  max_duration_sec: null,
  runner_memory: null,
  runner_cpus: null,
  telegram_thread_id: null,
  archived: false,
  created_at: '',
  updated_at: '',
  repositories: [],
}

describe('patchBody', () => {
  it('sends nothing when nothing changed', () => {
    expect(patchBody(toFormValues(project), project)).toEqual({})
  })

  it('sends only changed fields and resets cleared overrides with "" / 0', () => {
    const v = { ...toFormValues(project), name: 'Vatary 2', model: '', max_turns: '', redis: true, max_budget_usd: '2.5' }
    expect(patchBody(v, project)).toEqual({ name: 'Vatary 2', model: '', max_turns: 0, services: ['postgres', 'redis'], max_budget_usd: 2.5 })
  })

  it('sends the whole env map when a variable changes', () => {
    const v = { ...toFormValues(project), env: [{ key: 'NODE_ENV', value: 'test' }, { key: 'LOG', value: '1' }] }
    expect(patchBody(v, project)).toEqual({ env: { NODE_ENV: 'test', LOG: '1' } })
  })
})

describe('createBody', () => {
  it('omits empty overrides', () => {
    const v = { ...toFormValues(), id: 'new', name: 'New' }
    expect(createBody(v)).toEqual({ id: 'new', name: 'New', services: [], env: {} })
  })
})
