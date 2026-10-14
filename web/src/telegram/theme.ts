import { supports, type WebApp } from './webapp'

type ThemeKey = keyof WebApp['themeParams']

// Telegram themeParams → the shadcn/Tailwind CSS variables in src/index.css.
const MAP: Array<[cssVar: string, ...keys: ThemeKey[]]> = [
  ['--background', 'bg_color'],
  ['--foreground', 'text_color'],
  ['--card', 'section_bg_color', 'bg_color'],
  ['--card-foreground', 'text_color'],
  ['--popover', 'section_bg_color', 'bg_color'],
  ['--popover-foreground', 'text_color'],
  ['--surface', 'secondary_bg_color'],
  ['--secondary', 'secondary_bg_color'],
  ['--secondary-foreground', 'text_color'],
  ['--muted', 'secondary_bg_color'],
  ['--muted-foreground', 'hint_color', 'subtitle_text_color'],
  ['--primary', 'button_color'],
  ['--primary-foreground', 'button_text_color'],
  ['--ring', 'accent_text_color', 'button_color'],
  ['--link', 'link_color'],
  ['--destructive', 'destructive_text_color'],
  ['--border', 'section_separator_color'],
  ['--input', 'section_separator_color'],
]

/** Applies the Telegram theme to <html> and the native chrome. Returns the chosen scheme. */
export function applyTelegramTheme(tg: WebApp, root: HTMLElement = document.documentElement) {
  const params = tg.themeParams ?? {}
  for (const [cssVar, ...keys] of MAP) {
    const value = keys.map((k) => params[k]).find(Boolean)
    if (value) root.style.setProperty(cssVar, value)
    else root.style.removeProperty(cssVar)
  }
  // An accent tinted from the button colour for subtle surfaces.
  if (params.button_color) root.style.setProperty('--accent', `color-mix(in oklab, ${params.button_color} 14%, transparent)`)
  if (!params.section_separator_color) {
    root.style.setProperty('--border', 'color-mix(in oklab, currentColor 12%, transparent)')
    root.style.setProperty('--input', 'color-mix(in oklab, currentColor 16%, transparent)')
  }
  root.classList.toggle('dark', tg.colorScheme === 'dark')
  root.style.colorScheme = tg.colorScheme

  try {
    if (supports(tg, 'headerColor')) {
      tg.setHeaderColor(params.bg_color ?? 'bg_color')
      tg.setBackgroundColor(params.bg_color ?? 'bg_color')
    }
    if (supports(tg, 'bottomBarColor')) tg.setBottomBarColor(params.bg_color ?? 'bg_color')
  } catch {
    // Older clients reject custom colours; the default chrome is fine.
  }
}
