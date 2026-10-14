import { setupWorker } from 'msw/browser'
import { handlers } from './handlers'
import { startSimulator } from './simulator'

/** Mock mode: MSW service worker for REST, MSW WebSocket interception for /api/v1/ws. */
export async function startMocks() {
  const worker = setupWorker(...handlers)
  await worker.start({ onUnhandledFrame: 'bypass', quiet: true })
  startSimulator()
  installFakeTelegram()
}

/** ?tg=1 in mock mode pretends to run inside Telegram (no real WebApp). */
function installFakeTelegram() {
  const params = new URLSearchParams(window.location.search)
  if (params.get('tg') !== '1') return
  const noop = () => {}
  const button = {
    text: '',
    isVisible: false,
    setText(t: string) {
      this.text = t
      return this
    },
    show() {
      this.isVisible = true
      return this
    },
    hide() {
      this.isVisible = false
      return this
    },
    enable: () => button,
    disable: () => button,
    showProgress: () => button,
    hideProgress: () => button,
    onClick: () => button,
    offClick: () => button,
  }
  const dark = params.get('dark') === '1'
  const fake = {
    initData: 'query_id=mock&user=%7B%22id%22%3A1%7D&auth_date=9999999999&hash=mock',
    initDataUnsafe: { user: { id: 1, first_name: 'Mock' }, auth_date: Math.floor(Date.now() / 1000), start_param: params.get('startapp') ?? undefined },
    version: '8.0',
    platform: 'mock',
    colorScheme: dark ? 'dark' : 'light',
    themeParams: dark
      ? { bg_color: '#17212b', text_color: '#f5f5f5', hint_color: '#708499', link_color: '#6ab3f3', button_color: '#5288c1', button_text_color: '#ffffff', secondary_bg_color: '#232e3c', section_bg_color: '#17212b', destructive_text_color: '#ec3942', accent_text_color: '#6ab2f2' }
      : { bg_color: '#ffffff', text_color: '#000000', hint_color: '#999999', link_color: '#2481cc', button_color: '#2481cc', button_text_color: '#ffffff', secondary_bg_color: '#efeff3', section_bg_color: '#ffffff', destructive_text_color: '#e53935', accent_text_color: '#2481cc' },
    viewportHeight: window.innerHeight,
    viewportStableHeight: window.innerHeight,
    safeAreaInset: { top: 0, bottom: 0, left: 0, right: 0 },
    contentSafeAreaInset: { top: 0, bottom: 0, left: 0, right: 0 },
    isVersionAtLeast: (v: string) => Number(v) <= 8,
    ready: noop,
    expand: noop,
    onEvent: noop,
    offEvent: noop,
    setHeaderColor: noop,
    setBackgroundColor: noop,
    setBottomBarColor: noop,
    enableClosingConfirmation: noop,
    disableClosingConfirmation: noop,
    disableVerticalSwipes: noop,
    enableVerticalSwipes: noop,
    showConfirm: (msg: string, cb: (ok: boolean) => void) => cb(window.confirm(msg)),
    MainButton: button,
    SecondaryButton: { ...button },
    BackButton: { show: noop, hide: noop, onClick: noop, offClick: noop },
    HapticFeedback: { impactOccurred: noop, notificationOccurred: noop, selectionChanged: noop },
  }
  ;(window as unknown as { Telegram: { WebApp: unknown } }).Telegram = { WebApp: fake }
}
