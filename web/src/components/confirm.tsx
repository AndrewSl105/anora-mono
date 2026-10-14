import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from 'react'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { useRuntime } from '@/telegram/runtime'
import { supports } from '@/telegram/webapp'
import { t } from '@/i18n/en'

type Confirm = (message: string, okLabel?: string) => Promise<boolean>
const ConfirmContext = createContext<Confirm>(() => Promise.resolve(false))

/** Telegram showConfirm in the Mini App, an in-app dialog in a browser. */
export function ConfirmProvider({ children }: { children: ReactNode }) {
  const runtime = useRuntime()
  const [open, setOpen] = useState<{ message: string; ok: string } | null>(null)
  const resolver = useRef<(v: boolean) => void>(undefined)

  const confirm = useCallback<Confirm>(
    (message, okLabel = 'OK') => {
      if (runtime.mode === 'telegram' && supports(runtime.tg, 'popups')) {
        return new Promise((resolve) => runtime.tg.showConfirm(message, (ok: boolean) => resolve(ok)))
      }
      return new Promise((resolve) => {
        resolver.current = resolve
        setOpen({ message, ok: okLabel })
      })
    },
    [runtime],
  )

  function close(result: boolean) {
    resolver.current?.(result)
    resolver.current = undefined
    setOpen(null)
  }

  return (
    <ConfirmContext.Provider value={confirm}>
      {children}
      <AlertDialog open={!!open} onOpenChange={(o) => !o && close(false)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{open?.ok}</AlertDialogTitle>
            <AlertDialogDescription>{open?.message}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel onClick={() => close(false)}>{t.common.cancel}</AlertDialogCancel>
            <AlertDialogAction onClick={() => close(true)}>{open?.ok}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </ConfirmContext.Provider>
  )
}

export function useConfirm(): Confirm {
  return useContext(ConfirmContext)
}
