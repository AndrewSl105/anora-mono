import type { ReactNode } from 'react'
import type { TaskView } from '@/api/types'
import type { RunInfo } from '@/lib/timeline'
import { dateTime, duration, usd } from '@/lib/format'
import { t } from '@/i18n/en'

const ti = t.task.info

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex justify-between gap-4 py-2 text-sm">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 text-right break-words">{children}</dd>
    </div>
  )
}

export function InfoTab({ task, runs }: { task: TaskView; runs: RunInfo[] }) {
  return (
    <div className="min-h-0 flex-1 overflow-y-auto px-4 pt-4 pb-[calc(var(--safe-bottom)+1rem)]">
      <section className="mb-6">
        <h2 className="mb-2 text-sm font-medium">{ti.prompt}</h2>
        <p className="rounded-xl bg-muted px-3.5 py-3 text-sm whitespace-pre-wrap">{task.prompt}</p>
      </section>

      <dl className="mb-6 divide-y rounded-xl border px-3.5">
        <Row label={ti.created}>{dateTime(task.created_at)}</Row>
        <Row label={ti.updated}>{dateTime(task.updated_at)}</Row>
        {task.finished_at && <Row label={ti.finished}>{dateTime(task.finished_at)}</Row>}
        {task.paused_at && <Row label={ti.paused}>{dateTime(task.paused_at)}</Row>}
        {task.status_reason && <Row label={ti.reason}>{task.status_reason}</Row>}
        <Row label={ti.source}>{task.source}</Row>
        {task.ci_status && <Row label={ti.ci}>{task.ci_status}</Row>}
        {task.branch && (
          <Row label="Branch">
            <span className="font-mono text-xs">{task.branch}</span>
          </Row>
        )}
      </dl>

      <section>
        <h2 className="mb-2 text-sm font-medium">{ti.runs}</h2>
        {runs.length === 0 ? (
          <p className="text-sm text-muted-foreground">{ti.noRuns}</p>
        ) : (
          <div className="overflow-x-auto rounded-xl border">
            <table className="w-full text-sm">
              <thead className="text-left text-xs text-muted-foreground">
                <tr className="border-b">
                  <th className="px-3 py-2 font-medium">#</th>
                  <th className="px-3 py-2 font-medium">Kind</th>
                  <th className="px-3 py-2 font-medium">Duration</th>
                  <th className="px-3 py-2 text-right font-medium">Cost</th>
                  <th className="px-3 py-2 text-right font-medium">Exit</th>
                </tr>
              </thead>
              <tbody className="tabular-nums">
                {runs.map((run) => (
                  <tr key={run.runId} className="border-b last:border-0">
                    <td className="px-3 py-2">{run.runId}</td>
                    <td className="px-3 py-2">{run.kind}</td>
                    <td className="px-3 py-2">{duration(run.startedAt, run.finishedAt)}</td>
                    <td className="px-3 py-2 text-right">{run.cost !== undefined ? usd(run.cost) : '—'}</td>
                    <td className="px-3 py-2 text-right">{run.finishedAt ? (run.exitCode ?? '—') : '…'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  )
}
