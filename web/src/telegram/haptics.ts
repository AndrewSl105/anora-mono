import { getWebApp, detectMode, supports } from './webapp'

function feedback() {
  const tg = getWebApp()
  return detectMode(tg) === 'telegram' && supports(tg, 'haptics') ? tg!.HapticFeedback : undefined
}

/** Haptics are a no-op outside Telegram. */
export const haptic = {
  tap: () => feedback()?.impactOccurred('light'),
  success: () => feedback()?.notificationOccurred('success'),
  error: () => feedback()?.notificationOccurred('error'),
  select: () => feedback()?.selectionChanged(),
}
