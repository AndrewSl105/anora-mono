import { describe, expect, it } from 'vitest'
import { parseDeepLink } from './deepLink'

describe('parseDeepLink', () => {
  it('reads task_<id> from start_param', () => {
    expect(parseDeepLink('task_123', '')).toBe(123)
  })

  it('reads ?task=<id> from the URL', () => {
    expect(parseDeepLink(undefined, '?task=42')).toBe(42)
    expect(parseDeepLink('', '?foo=1&task=7')).toBe(7)
  })

  it('prefers start_param over the query', () => {
    expect(parseDeepLink('task_5', '?task=9')).toBe(5)
  })

  it('ignores anything else', () => {
    expect(parseDeepLink('project_5', '')).toBeNull()
    expect(parseDeepLink('task_', '?task=abc')).toBeNull()
    expect(parseDeepLink('task_12x', '?task=1e3')).toBeNull()
    expect(parseDeepLink(null, '')).toBeNull()
  })
})
