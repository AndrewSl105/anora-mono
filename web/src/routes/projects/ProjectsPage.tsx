import { useMemo } from 'react'
import { Link } from 'react-router'
import { ChevronRight, FolderGit2, Plus } from 'lucide-react'
import { useProjects, useTasks } from '@/api/hooks'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { t } from '@/i18n/en'

export default function ProjectsPage() {
  const projects = useProjects()
  // The Project object has no active task count; count the active list client-side.
  const active = useTasks({ group: 'active' })
  const activeByProject = useMemo(() => {
    const m = new Map<string, number>()
    for (const task of active.data?.pages.flatMap((p) => p.items) ?? []) m.set(task.project_id, (m.get(task.project_id) ?? 0) + 1)
    return m
  }, [active.data])

  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto max-w-2xl px-4 pt-4 pb-6">
        <div className="mb-4 flex items-center justify-between">
          <h1 className="text-xl font-semibold">{t.projects.title}</h1>
          <Button asChild variant="outline" className="h-10">
            <Link to="/projects/new">
              <Plus /> {t.projects.new}
            </Link>
          </Button>
        </div>
        {projects.isPending ? (
          <div className="space-y-2">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-16 rounded-xl" />
            ))}
          </div>
        ) : !projects.data?.length ? (
          <p className="py-10 text-center text-sm text-muted-foreground">{t.projects.empty}</p>
        ) : (
          <ul className="divide-y rounded-xl border">
            {projects.data.map((p) => {
              const n = activeByProject.get(p.id) ?? 0
              return (
                <li key={p.id}>
                  <Link to={`/projects/${p.id}`} className="flex min-h-16 items-center gap-3 px-4 py-3 hover:bg-accent/60">
                    <FolderGit2 className="size-5 shrink-0 text-muted-foreground" aria-hidden="true" />
                    <div className="min-w-0 flex-1">
                      <div className="truncate font-medium">{p.name}</div>
                      <div className="text-xs text-muted-foreground">
                        <span className="font-mono">{p.id}</span> · {t.projects.repos(p.repositories.length)}
                        {n > 0 && <span className="text-primary"> · {t.projects.active(n)}</span>}
                      </div>
                    </div>
                    <ChevronRight className="size-4 text-muted-foreground" aria-hidden="true" />
                  </Link>
                </li>
              )
            })}
          </ul>
        )}
      </div>
    </div>
  )
}
