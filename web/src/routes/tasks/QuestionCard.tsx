import { useState } from 'react'
import { CircleHelp, Send } from 'lucide-react'
import type { TimelineItem } from '@/lib/timeline'
import { useAnswer } from '@/api/hooks'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { PrimaryAction, useClosingConfirmation } from '@/telegram/controls'
import { useRuntime } from '@/telegram/runtime'
import { cn } from '@/lib/utils'
import { t } from '@/i18n/en'

type Props = { taskId: number; item: Extract<TimelineItem, { kind: 'question' }>; open: boolean }

export function QuestionCard({ taskId, item, open }: Props) {
  const runtime = useRuntime()
  const answer = useAnswer(taskId)
  const [text, setText] = useState('')
  const [focused, setFocused] = useState(false)
  useClosingConfirmation(open && text.trim().length > 0)

  const answered = item.answer
  const interactive = open && !answered && !answer.isPending

  function send(value: string) {
    const v = value.trim()
    if (!v || !interactive) return
    answer.mutate({ question_id: item.questionId, text: v }, { onSuccess: () => setText('') })
  }

  return (
    <section
      aria-label={t.task.question.title}
      className={cn(
        'rounded-2xl border px-3.5 py-3',
        open && !answered ? 'border-amber-500/60 bg-amber-500/8 shadow-sm' : 'bg-card',
      )}
    >
      <div className="mb-1.5 flex items-center gap-1.5 text-xs font-medium text-amber-700 dark:text-amber-300">
        <CircleHelp className="size-3.5" aria-hidden="true" />
        {t.task.question.title}
      </div>
      <p className="text-[15px] whitespace-pre-wrap">{item.text}</p>

      {item.options.length > 0 && (
        <div className="mt-3 flex flex-wrap gap-2">
          {item.options.map((option) => (
            <Button
              key={option}
              variant={answered?.text === option ? 'default' : 'outline'}
              className="h-11 max-w-full whitespace-normal"
              disabled={!interactive}
              onClick={() => send(option)}
            >
              {option}
            </Button>
          ))}
        </div>
      )}

      {answered ? (
        <p className="mt-3 text-xs text-muted-foreground">
          {answered.pending ? t.task.timeline.sending : t.task.question.answeredVia(answered.via)}
          {!item.options.includes(answered.text) && <span className="mt-1 block text-sm text-foreground">“{answered.text}”</span>}
        </p>
      ) : (
        open && (
          <div className="mt-3 flex items-end gap-2">
            <Textarea
              value={text}
              onChange={(e) => setText(e.target.value)}
              onFocus={() => setFocused(true)}
              onBlur={() => setTimeout(() => setFocused(false), 150)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                  e.preventDefault()
                  send(text)
                }
              }}
              placeholder={t.task.question.placeholder}
              aria-label={t.task.question.placeholder}
              rows={1}
              disabled={!interactive}
              className="max-h-40 min-h-11 resize-none bg-background"
            />
            {runtime.mode === 'browser' && (
              <Button
                size="icon"
                className="size-11 shrink-0"
                disabled={!interactive || !text.trim()}
                onClick={() => send(text)}
                aria-label={t.task.question.send}
              >
                <Send />
              </Button>
            )}
          </div>
        )
      )}
      {runtime.mode === 'telegram' && open && !answered && (focused || text.trim()) && (
        <PrimaryAction label={t.task.question.send} onClick={() => send(text)} loading={answer.isPending} disabled={!text.trim()} />
      )}
    </section>
  )
}
