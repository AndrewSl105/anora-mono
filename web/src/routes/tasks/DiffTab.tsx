import { useMemo, useState } from 'react'
import { Diff, Hunk, parseDiff, type FileData } from 'react-diff-view'
import 'react-diff-view/style/index.css'
import './diff.css'
import { ChevronRight, FileDiff } from 'lucide-react'
import { useTaskDiff } from '@/api/hooks'
import { isApiError } from '@/api/errors'
import { PageSpinner } from '@/components/PageSpinner'
import { useIsDesktop } from '@/telegram/runtime'
import { cn } from '@/lib/utils'
import { t } from '@/i18n/en'

function fileName(f: FileData) {
  return f.type === 'delete' ? f.oldPath : f.newPath
}

function counts(f: FileData) {
  let add = 0
  let del = 0
  for (const h of f.hunks) {
    for (const c of h.changes) {
      if (c.type === 'insert') add++
      else if (c.type === 'delete') del++
    }
  }
  return { add, del }
}

function FileBlock({ file, split }: { file: FileData; split: boolean }) {
  const [open, setOpen] = useState(true)
  const { add, del } = counts(file)
  const name = fileName(file)
  return (
    <section id={`file-${name}`} className="overflow-hidden rounded-xl border">
      <button
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="flex min-h-11 w-full items-center gap-2 bg-muted/60 px-3 text-left text-xs"
      >
        <ChevronRight className={cn('size-3.5 shrink-0 transition-transform', open && 'rotate-90')} aria-hidden="true" />
        <span className="min-w-0 flex-1 truncate font-mono">{name}</span>
        <span className="font-mono text-emerald-700 dark:text-emerald-300">+{add}</span>
        <span className="font-mono text-red-700 dark:text-red-300">−{del}</span>
      </button>
      {open && (
        <div className="diff-scroll overflow-x-auto text-xs">
          {file.isBinary ? (
            <p className="px-3 py-2 text-muted-foreground">Binary file</p>
          ) : (
            <Diff viewType={split ? 'split' : 'unified'} diffType={file.type} hunks={file.hunks}>
              {(hunks) => hunks.map((h) => <Hunk key={h.content} hunk={h} />)}
            </Diff>
          )}
        </div>
      )}
    </section>
  )
}

export default function DiffTab({ taskId }: { taskId: number }) {
  const diff = useTaskDiff(taskId, true)
  const desktop = useIsDesktop()
  const files = useMemo(() => {
    if (!diff.data?.diff) return []
    try {
      return parseDiff(diff.data.diff, { nearbySequences: 'zip' })
    } catch {
      return []
    }
  }, [diff.data])

  if (diff.isPending) return <PageSpinner />
  if (diff.isError) {
    return (
      <p className="px-6 py-10 text-center text-sm text-muted-foreground">
        {isApiError(diff.error, 'no_workspace') ? t.task.diff.noWorkspace : t.common.somethingWrong}
      </p>
    )
  }
  if (files.length === 0) return <p className="px-6 py-10 text-center text-sm text-muted-foreground">{t.task.diff.empty}</p>

  return (
    <div className="min-h-0 flex-1 overflow-y-auto px-4 pt-3 pb-[calc(var(--safe-bottom)+1rem)]">
      <div className="mb-3 flex items-center gap-2 text-xs text-muted-foreground">
        <FileDiff className="size-4" aria-hidden="true" />
        {t.task.diff.files(files.length)}
        <span className="truncate font-mono">
          {diff.data.base}…{diff.data.branch}
        </span>
      </div>
      <ul className="mb-4 divide-y rounded-xl border text-xs">
        {files.map((f) => {
          const { add, del } = counts(f)
          const name = fileName(f)
          return (
            <li key={name}>
              <a href={`#file-${name}`} className="flex min-h-10 items-center gap-2 px-3 hover:bg-muted">
                <span className="min-w-0 flex-1 truncate font-mono">{name}</span>
                <span className="font-mono text-emerald-700 dark:text-emerald-300">+{add}</span>
                <span className="font-mono text-red-700 dark:text-red-300">−{del}</span>
              </a>
            </li>
          )
        })}
      </ul>
      {diff.data.truncated && <p className="mb-3 text-xs text-amber-700 dark:text-amber-300">{t.task.diff.truncated}</p>}
      <div className="space-y-3">
        {files.map((f) => (
          <FileBlock key={fileName(f)} file={f} split={desktop} />
        ))}
      </div>
    </div>
  )
}
