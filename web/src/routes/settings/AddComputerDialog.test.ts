import { describe, expect, it } from 'vitest'
import { formatCode } from './AddComputerDialog'

describe('formatCode', () => {
  it('uppercases, drops other characters and adds the dash', () => {
    expect(formatCode('kq74mx')).toBe('KQ7-4MX')
    expect(formatCode(' kq7 4mx ')).toBe('KQ7-4MX')
    expect(formatCode('kq')).toBe('KQ')
    expect(formatCode('KQ7-4MX-EXTRA')).toBe('KQ7-4MX')
  })
})
