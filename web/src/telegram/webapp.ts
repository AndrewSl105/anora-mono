import type { WebApp as TelegramWebApp } from 'telegram-web-app'

export type WebApp = TelegramWebApp

/** Minimum Bot API versions per feature (core.telegram.org/bots/webapps). */
const MIN_VERSION = {
  backButton: '6.1',
  haptics: '6.1',
  headerColor: '6.1',
  closingConfirmation: '6.2',
  popups: '6.2',
  verticalSwipes: '7.7',
  secondaryButton: '7.10',
  bottomBarColor: '7.10',
  safeArea: '8.0',
} as const

export type Feature = keyof typeof MIN_VERSION

/** The WebApp object when the official script loaded, regardless of mode. */
export function getWebApp(): WebApp | undefined {
  if (typeof window === 'undefined') return undefined
  return (window as { Telegram?: { WebApp?: WebApp } }).Telegram?.WebApp
}

/** Telegram mode = the WebApp object exists and carries a non-empty initData. */
export function detectMode(tg: WebApp | undefined = getWebApp()): 'telegram' | 'browser' {
  return tg && typeof tg.initData === 'string' && tg.initData.length > 0 ? 'telegram' : 'browser'
}

export function supports(tg: WebApp | undefined, feature: Feature): boolean {
  if (!tg) return false
  try {
    return tg.isVersionAtLeast(MIN_VERSION[feature])
  } catch {
    return false
  }
}
