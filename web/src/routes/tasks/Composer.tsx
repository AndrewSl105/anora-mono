import { useState } from 'react'
import { ArrowUp } from 'lucide-react'
import type { TaskView } from '@/api/types'
import { useSendMessage } from '@/api/hooks'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { composerMode } from '@/lib/status'
import { useClosingConfirmation } from '@/telegram/controls'
import { t } from '@/i18n/en'

export function Composer({ task }: { task: TaskView }) {
  const mode = composerMode(task.status)
  const send = useSendMessage(task.id)
  const [text, setText] = useState('')
  useClosingConfirmation(!!mode && text.trim().length > 0)

  if (!mode) return null

  function submit() {
    const v = text.trim()
    if (!v || send.isPending) return
    setText('')
    send.mutate({ text: v, queued: mode === 'queue' }, { onError: () => setText(v) })
  }

  const label = mode === 'queue' ? t.task.composer.queue : t.task.composer.followup
  return (
    <div className="shrink-0 border-t bg-background/95 px-3 pt-2 pb-[calc(var(--safe-bottom)+0.5rem)] backdrop-blur">
      {task.queued_message ? (
        <p className="mb-2 line-clamp-2 rounded-lg bg-muted px-3 py-1.5 text-xs text-muted-foreground">
          <span className="font-medium text-foreground/80">{t.task.composer.queued}:</span> {task.queued_message}
        </p>
      ) : (
        mode === 'queue' && <p className="mb-1.5 px-1 text-xs text-muted-foreground">{t.task.composer.queueHint}</p>
      )}
      <form
        className="flex items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          submit()
        }}
      >
        <Textarea
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
              e.preventDefault()
              submit()
            }
          }}
          rows={1}
          placeholder={label}
          aria-label={label}
          className="max-h-40 min-h-11 resize-none rounded-2xl"
        />
        <Button
          type="submit"
          size="icon"
          className="size-11 shrink-0 rounded-full"
          disabled={!text.trim() || send.isPending}
          aria-label={t.common.send}
        >
          <ArrowUp />
        </Button>
      </form>
    </div>
  )
}
