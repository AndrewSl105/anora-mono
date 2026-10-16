import { useEffect, useRef, useState } from 'react'
import { ImageOff, Loader2 } from 'lucide-react'
import { useAttachmentImage } from '@/api/hooks'
import type { Attachment } from '@/api/types'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { cn } from '@/lib/utils'
import { t } from '@/i18n/en'

const THUMB = 112 // px, longest side in the bubble

function thumbSize(a: Attachment): { width: number; height: number } {
  const w = Math.max(1, a.width)
  const h = Math.max(1, a.height)
  const scale = THUMB / Math.max(w, h)
  return {
    width: Math.max(48, Math.round(w * scale)),
    height: Math.max(48, Math.round(h * scale)),
  }
}

/** Thumbnails of a message's images; tap one to see it full size. */
export function AttachmentGrid({ taskId, items, className }: { taskId: number; items: Attachment[]; className?: string }) {
  const [open, setOpen] = useState<Attachment | null>(null)
  if (items.length === 0) return null
  return (
    <>
      <ul className={cn('flex flex-wrap justify-end gap-1.5', className)}>
        {items.map((a) => (
          <li key={a.id}>
            <Thumb taskId={taskId} a={a} onOpen={() => setOpen(a)} />
          </li>
        ))}
      </ul>
      <Dialog open={open !== null} onOpenChange={(o) => !o && setOpen(null)}>
        <DialogContent className="max-h-[95dvh] w-[95vw] max-w-[95vw] p-2 sm:max-w-5xl" aria-describedby={undefined}>
          <DialogTitle className="sr-only">{open?.name ?? ''}</DialogTitle>
          {open && <FullImage taskId={taskId} a={open} />}
        </DialogContent>
      </Dialog>
    </>
  )
}

function Thumb({ taskId, a, onOpen }: { taskId: number; a: Attachment; onOpen: () => void }) {
  const img = useAttachmentImage(taskId, a.id, true)
  const size = thumbSize(a)
  if (img.state === 'removed' || img.state === 'error') {
    return (
      <div
        style={size}
        className="flex flex-col items-center justify-center gap-1 rounded-lg border border-dashed border-current/40 px-1 text-center text-[11px] leading-tight opacity-80"
      >
        <ImageOff className="size-4" aria-hidden="true" />
        {img.state === 'removed' ? t.attachments.removed : t.attachments.loadFailed}
      </div>
    )
  }
  return (
    <button
      type="button"
      onClick={onOpen}
      disabled={img.state !== 'ready'}
      aria-label={t.attachments.open(a.name)}
      style={size}
      className="flex items-center justify-center overflow-hidden rounded-lg bg-black/10"
    >
      {img.state === 'ready' ? (
        <BlobImage blob={img.blob} alt={a.name} className="size-full object-cover" />
      ) : (
        <Loader2 className="size-4 animate-spin opacity-70" aria-hidden="true" />
      )}
    </button>
  )
}

function FullImage({ taskId, a }: { taskId: number; a: Attachment }) {
  const img = useAttachmentImage(taskId, a.id, false)
  if (img.state === 'ready') {
    return (
      <BlobImage blob={img.blob} alt={a.name} className="mx-auto max-h-[90dvh] w-auto max-w-full rounded-md object-contain" />
    )
  }
  return (
    <div className="flex h-60 flex-col items-center justify-center gap-2 text-sm text-muted-foreground">
      {img.state === 'loading' ? (
        <Loader2 className="size-5 animate-spin" aria-hidden="true" />
      ) : (
        <>
          <ImageOff className="size-5" aria-hidden="true" />
          {img.state === 'removed' ? t.attachments.removed : t.attachments.loadFailed}
        </>
      )}
    </div>
  )
}

/** An <img> showing a Blob; its object URL lives exactly as long as the element shows it. */
function BlobImage({ blob, ...props }: { blob: Blob } & Omit<React.ComponentProps<'img'>, 'src'>) {
  const ref = useRef<HTMLImageElement>(null)
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const url = URL.createObjectURL(blob)
    el.src = url
    return () => {
      el.removeAttribute('src')
      URL.revokeObjectURL(url)
    }
  }, [blob])
  return <img ref={ref} {...props} />
}
