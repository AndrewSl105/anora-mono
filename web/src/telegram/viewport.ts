import { supports, type WebApp } from './webapp'

/** Stable viewport height and safe-area insets as CSS variables (not 100vh). */
export function applyViewport(tg: WebApp, root: HTMLElement = document.documentElement) {
  if (tg.viewportStableHeight) root.style.setProperty('--app-height', `${tg.viewportStableHeight}px`)
  let top = 0
  let bottom = 0
  if (supports(tg, 'safeArea')) {
    top = (tg.safeAreaInset?.top ?? 0) + (tg.contentSafeAreaInset?.top ?? 0)
    bottom = (tg.safeAreaInset?.bottom ?? 0) + (tg.contentSafeAreaInset?.bottom ?? 0)
  }
  root.style.setProperty('--safe-top', `${top}px`)
  root.style.setProperty('--safe-bottom', bottom ? `${bottom}px` : 'env(safe-area-inset-bottom, 0px)')
}
