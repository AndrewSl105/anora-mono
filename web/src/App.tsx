import { lazy, Suspense, useState } from 'react'
import { createBrowserRouter, RouterProvider } from 'react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Toaster } from '@/components/ui/sonner'
import { ErrorBoundary, CrashScreen } from '@/components/ErrorBoundary'
import { AppShell } from '@/components/AppShell'
import { ConfirmProvider } from '@/components/confirm'
import { AuthGate } from '@/auth/AuthGate'
import { RealtimeProvider } from '@/realtime/RealtimeProvider'
import { RuntimeProvider, type Runtime } from '@/telegram/runtime'
import { ThemeProvider } from '@/lib/theme'
import { isApiError } from '@/api/errors'
import { TasksLayout } from '@/routes/tasks/TasksLayout'
import { NoTaskSelected } from '@/routes/tasks/NoTaskSelected'
import { NotFound } from '@/routes/NotFound'
import { PageSpinner } from '@/components/PageSpinner'

const TaskDetailPage = lazy(() => import('@/routes/tasks/TaskDetailPage'))
const NewTaskPage = lazy(() => import('@/routes/NewTaskPage'))
const ProjectsPage = lazy(() => import('@/routes/projects/ProjectsPage'))
const ProjectEditPage = lazy(() => import('@/routes/projects/ProjectEditPage'))
const SettingsPage = lazy(() => import('@/routes/SettingsPage'))
const CanvasPage = lazy(() => import('@/routes/canvas/CanvasPage'))

function lazyPage(Page: React.ComponentType) {
  return (
    <Suspense fallback={<PageSpinner />}>
      <Page />
    </Suspense>
  )
}

export function createAppRouter() {
  return createBrowserRouter([
    {
      path: '/',
      element: <AppShell />,
      errorElement: <CrashScreen />,
      children: [
        {
          element: <TasksLayout />,
          children: [
            { index: true, element: <NoTaskSelected /> },
            { path: 'tasks/:id', element: lazyPage(TaskDetailPage) },
          ],
        },
        { path: 'new', element: lazyPage(NewTaskPage) },
        { path: 'canvas', element: lazyPage(CanvasPage) },
        { path: 'projects', element: lazyPage(ProjectsPage) },
        { path: 'projects/new', element: lazyPage(ProjectEditPage) },
        { path: 'projects/:id', element: lazyPage(ProjectEditPage) },
        { path: 'settings', element: lazyPage(SettingsPage) },
        { path: '*', element: <NotFound /> },
      ],
    },
  ])
}

export function createQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: (count, e) => !(isApiError(e) && e.status < 500) && count < 2,
        refetchOnWindowFocus: true,
      },
      mutations: { retry: false },
    },
  })
}

export function App({ runtime }: { runtime: Runtime }) {
  const [queryClient] = useState(createQueryClient)
  const [router] = useState(createAppRouter)
  return (
    <ErrorBoundary>
      <RuntimeProvider runtime={runtime}>
        <ThemeProvider>
          <QueryClientProvider client={queryClient}>
            <ConfirmProvider>
              <AuthGate>
                <RealtimeProvider>
                  <RouterProvider router={router} />
                </RealtimeProvider>
              </AuthGate>
            </ConfirmProvider>
            <Toaster position="top-center" offset={{ top: 'calc(var(--safe-top) + 12px)' }} />
          </QueryClientProvider>
        </ThemeProvider>
      </RuntimeProvider>
    </ErrorBoundary>
  )
}
