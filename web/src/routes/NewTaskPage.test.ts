import { describe, expect, it } from 'vitest'
import { toCreateBody } from './NewTaskPage'

describe('toCreateBody', () => {
  const base = { project_id: 'p', repository_id: '', title: '', prompt: ' Do it ', agent: '' as const }

  it('omits blank optional fields, including the agent (the project decides)', () => {
    expect(toCreateBody(base)).toEqual({ project_id: 'p', prompt: 'Do it' })
  })

  it('sends a picked agent', () => {
    expect(toCreateBody({ ...base, agent: 'codex' })).toEqual({ project_id: 'p', prompt: 'Do it', agent: 'codex' })
  })
})
