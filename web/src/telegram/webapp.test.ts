import { afterEach, describe, expect, it } from 'vitest'
import { detectMode, getWebApp, supports, type WebApp } from './webapp'
import { resolveRuntime } from './runtime'

const fake = (initData: string, version = '8.0') =>
  ({
    initData,
    version,
    isVersionAtLeast: (v: string) => Number(v) <= Number(version),
  }) as unknown as WebApp

afterEach(() => {
  delete (window as { Telegram?: unknown }).Telegram
})

describe('mode detection', () => {
  it('is browser without the WebApp object', () => {
    expect(getWebApp()).toBeUndefined()
    expect(detectMode()).toBe('browser')
    expect(resolveRuntime()).toEqual({ mode: 'browser' })
  })

  it('is browser when the script loaded outside Telegram (empty initData)', () => {
    ;(window as { Telegram?: unknown }).Telegram = { WebApp: fake('') }
    expect(detectMode()).toBe('browser')
  })

  it('is telegram with a non-empty initData', () => {
    const tg = fake('query_id=1&hash=abc')
    ;(window as { Telegram?: unknown }).Telegram = { WebApp: tg }
    expect(detectMode()).toBe('telegram')
    expect(resolveRuntime()).toEqual({ mode: 'telegram', tg })
  })
})

describe('supports', () => {
  it('gates features by WebApp version', () => {
    expect(supports(fake('x', '6.0'), 'backButton')).toBe(false)
    expect(supports(fake('x', '7.0'), 'backButton')).toBe(true)
    expect(supports(fake('x', '7.0'), 'safeArea')).toBe(false)
    expect(supports(undefined, 'haptics')).toBe(false)
  })
})
