import { toast } from 'sonner'
import { Copy } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { t } from '@/i18n/en'

export async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    toast.success(t.common.copied)
  } catch {
    toast.error(t.common.somethingWrong)
  }
}

export function CopyButton({ text, label = t.common.copy, className }: { text: string; label?: string; className?: string }) {
  return (
    <Button
      variant="ghost"
      size="icon-sm"
      className={cn('shrink-0 text-current hover:bg-current/10 hover:text-current', className)}
      onClick={() => void copyText(text)}
      aria-label={label}
      title={label}
    >
      <Copy className="size-4" />
    </Button>
  )
}
