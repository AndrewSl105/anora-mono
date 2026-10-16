import { ImagePlus, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { ACCEPT, formatBytes } from '@/lib/attachments'
import { cn } from '@/lib/utils'
import { t } from '@/i18n/en'
import type { ImagePicker } from './useImagePicker'

/** The attach button plus its hidden file input. */
export function AttachButton({ picker, disabled, className }: { picker: ImagePicker; disabled?: boolean; className?: string }) {
  return (
    <>
      <input {...picker.inputProps} accept={ACCEPT} data-testid="attach-input" />
      <Button
        type="button"
        variant="ghost"
        size="icon"
        className={cn('size-11 shrink-0 rounded-full', className)}
        onClick={picker.open}
        disabled={disabled}
        aria-label={t.attachments.attach}
        title={t.attachments.attach}
      >
        <ImagePlus />
      </Button>
    </>
  )
}

/**
 * Previews of the picked images with remove buttons, the upload progress
 * while sending, and the reasons files were refused.
 */
export function PickedImages({ picker, progress }: { picker: ImagePicker; progress: number | null }) {
  const { picked, errors } = picker
  if (picked.length === 0 && errors.length === 0 && progress === null) return null
  const uploading = progress !== null
  return (
    <div className="space-y-1.5">
      {picked.length > 0 && (
        <ul className="flex gap-2 overflow-x-auto pt-1 pb-1" aria-label={t.attachments.attach}>
          {picked.map((p) => (
            <li key={p.id} className="relative size-16 shrink-0">
              <img
                src={p.url}
                alt={p.file.name}
                title={`${p.file.name} · ${formatBytes(p.file.size)}`}
                className={cn('size-16 rounded-lg border object-cover', uploading && 'opacity-60')}
              />
              {!uploading && (
                <button
                  type="button"
                  onClick={() => picker.remove(p.id)}
                  aria-label={t.attachments.remove(p.file.name || t.attachments.pasted)}
                  // 44 px hit area around a small visible badge.
                  className="absolute -top-3 -right-3 flex size-11 items-center justify-center"
                >
                  <span className="flex size-6 items-center justify-center rounded-full bg-foreground/80 text-background shadow">
                    <X className="size-3.5" />
                  </span>
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
      {uploading && (
        <div className="space-y-1" role="status" aria-live="polite">
          <div className="h-1 overflow-hidden rounded-full bg-muted">
            <div className="h-full bg-primary transition-[width]" style={{ width: `${Math.round(progress * 100)}%` }} />
          </div>
          <p className="text-xs text-muted-foreground">{t.attachments.uploading(Math.round(progress * 100))}</p>
        </div>
      )}
      {errors.length > 0 && (
        <ul className="space-y-0.5 text-xs text-destructive" role="alert">
          {errors.map((e) => (
            <li key={e}>{e}</li>
          ))}
        </ul>
      )}
    </div>
  )
}

/** Overlay shown while files are dragged over a drop target. */
export function DropOverlay({ show }: { show: boolean }) {
  if (!show) return null
  return (
    <div className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center rounded-xl border-2 border-dashed border-primary bg-primary/5 text-sm font-medium text-primary">
      {t.attachments.dropHere}
    </div>
  )
}
