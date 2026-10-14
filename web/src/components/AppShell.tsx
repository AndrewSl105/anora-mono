import { NavLink, Outlet, useLocation, useNavigate } from 'react-router'
import { FolderGit2, ListTodo, Plus, Settings } from 'lucide-react'
import { lazy, Suspense } from 'react'
import { cn } from '@/lib/utils'
import { t } from '@/i18n/en'
import { haptic } from '@/telegram/haptics'
import { useBackButton } from '@/telegram/controls'
import { DeepLink } from '@/telegram/DeepLinkHandler'
import { Logo } from './Logo'
import { OfflineBanner } from './OfflineBanner'

const DebugPanel = import.meta.env.VITE_DEBUG === '1' ? lazy(() => import('@/telegram/DebugPanel')) : null

const TAB_ROOTS = ['/', '/new', '/projects', '/settings']

const NAV = [
  { to: '/', label: t.nav.tasks, icon: ListTodo, end: false },
  { to: '/new', label: t.nav.new, icon: Plus, end: true, primary: true },
  { to: '/projects', label: t.nav.projects, icon: FolderGit2, end: false },
  { to: '/settings', label: t.nav.settings, icon: Settings, end: true },
] as const

function isActive(pathname: string, to: string) {
  if (to === '/') return pathname === '/' || pathname.startsWith('/tasks')
  return pathname === to || pathname.startsWith(`${to}/`)
}

export function parentPath(pathname: string): string {
  if (pathname.startsWith('/projects/')) return '/projects'
  return '/'
}

export function AppShell() {
  const { pathname } = useLocation()
  const navigate = useNavigate()
  const deep = !TAB_ROOTS.includes(pathname)

  useBackButton(deep, () => {
    const idx = (window.history.state as { idx?: number } | null)?.idx ?? 0
    if (idx > 0) void navigate(-1)
    else void navigate(parentPath(pathname))
  })

  return (
    <div className="flex h-(--app-height) overflow-hidden bg-background pt-(--safe-top)">
      <DeepLink />
      {/* Desktop sidebar */}
      <aside className="hidden w-56 shrink-0 flex-col border-r bg-surface lg:flex">
        <div className="flex items-center gap-2 px-4 py-4">
          <Logo className="size-7" />
          <span className="font-semibold">{t.app.name}</span>
        </div>
        <nav className="flex flex-col gap-1 px-2" aria-label="Main">
          {NAV.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              className={cn(
                'flex h-10 items-center gap-3 rounded-md px-3 text-sm font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-foreground',
                isActive(pathname, item.to) && 'bg-accent text-foreground',
              )}
            >
              <item.icon className="size-4" aria-hidden="true" />
              {item.label}
            </NavLink>
          ))}
        </nav>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <OfflineBanner />
        <div className="min-h-0 flex-1">
          <Outlet />
        </div>
        {/* Mobile bottom tabs, only on tab roots */}
        {!deep && (
          <nav
            aria-label="Main"
            className="grid shrink-0 grid-cols-4 border-t bg-background/95 pb-(--safe-bottom) backdrop-blur lg:hidden"
          >
            {NAV.map((item) => {
              const active = isActive(pathname, item.to)
              return (
                <NavLink
                  key={item.to}
                  to={item.to}
                  onClick={() => haptic.select()}
                  className={cn(
                    'flex min-h-14 flex-col items-center justify-center gap-0.5 text-[11px] font-medium text-muted-foreground',
                    active && 'text-primary',
                  )}
                >
                  {'primary' in item ? (
                    <span className="flex size-9 items-center justify-center rounded-full bg-primary text-primary-foreground shadow-sm">
                      <item.icon className="size-5" aria-hidden="true" />
                    </span>
                  ) : (
                    <item.icon className="size-5" aria-hidden="true" />
                  )}
                  <span className={cn('primary' in item && 'sr-only')}>{item.label}</span>
                </NavLink>
              )
            })}
          </nav>
        )}
      </div>
      {DebugPanel && (
        <Suspense>
          <DebugPanel />
        </Suspense>
      )}
    </div>
  )
}
