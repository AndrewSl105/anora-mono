import { useEffect, useRef } from 'react'
import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { useRuntime } from './runtime'
import { supports } from './webapp'
import { haptic } from './haptics'

type PrimaryActionProps = {
  label: string
  onClick: () => void
  loading?: boolean
  disabled?: boolean
  className?: string
  /** Optional secondary action (Telegram SecondaryButton when supported, otherwise an outline button). */
  secondary?: { label: string; onClick: () => void; destructive?: boolean }
}

/** The screen's primary action: Telegram MainButton in the Mini App, a normal button in a browser. */
export function PrimaryAction({ label, onClick, loading, disabled, className, secondary }: PrimaryActionProps) {
  const runtime = useRuntime()
  const clickRef = useRef(onClick)
  const secondaryRef = useRef(secondary?.onClick)
  useEffect(() => {
    clickRef.current = onClick
    secondaryRef.current = secondary?.onClick
  })

  const tg = runtime.mode === 'telegram' ? runtime.tg : undefined

  useEffect(() => {
    if (!tg) return
    const button = tg.MainButton
    const handler = () => {
      haptic.tap()
      clickRef.current()
    }
    button.onClick(handler)
    button.show()
    return () => {
      button.offClick(handler)
      button.hideProgress()
      button.hide()
    }
  }, [tg])

  useEffect(() => {
    if (!tg) return
    const button = tg.MainButton
    button.setText(label)
    if (disabled) button.disable()
    else button.enable()
    if (loading) button.showProgress(false)
    else button.hideProgress()
  }, [tg, label, disabled, loading])

  const hasSecondary = !!secondary
  const nativeSecondary = !!tg && supports(tg, 'secondaryButton')
  useEffect(() => {
    if (!tg || !hasSecondary || !nativeSecondary) return
    const button = tg.SecondaryButton
    const handler = () => secondaryRef.current?.()
    button.onClick(handler)
    button.show()
    return () => {
      button.offClick(handler)
      button.hide()
    }
  }, [tg, hasSecondary, nativeSecondary])
  useEffect(() => {
    if (tg && secondary && nativeSecondary) tg.SecondaryButton.setText(secondary.label)
  }, [tg, secondary, nativeSecondary])

  if (tg) {
    if (!secondary || nativeSecondary) return null
    return (
      <Button variant="outline" className={cn('h-11 w-full', className)} onClick={secondary.onClick}>
        {secondary.label}
      </Button>
    )
  }

  return (
    <div className={cn('flex gap-2', className)}>
      {secondary && (
        <Button
          type="button"
          variant={secondary.destructive ? 'destructive' : 'outline'}
          className="h-11"
          onClick={secondary.onClick}
        >
          {secondary.label}
        </Button>
      )}
      <Button
        type="button"
        className="h-11 flex-1"
        disabled={disabled || loading}
        onClick={() => {
          haptic.tap()
          onClick()
        }}
      >
        {loading && <Loader2 className="animate-spin" />}
        {label}
      </Button>
    </div>
  )
}

/** Telegram BackButton on screens deeper than a tab root. */
export function useBackButton(visible: boolean, onBack: () => void) {
  const runtime = useRuntime()
  const backRef = useRef(onBack)
  useEffect(() => {
    backRef.current = onBack
  })
  const tg = runtime.mode === 'telegram' && supports(runtime.tg, 'backButton') ? runtime.tg : undefined
  useEffect(() => {
    if (!tg) return
    const handler = () => backRef.current()
    tg.BackButton.onClick(handler)
    return () => {
      tg.BackButton.offClick(handler)
    }
  }, [tg])
  useEffect(() => {
    if (!tg) return
    if (visible) tg.BackButton.show()
    else tg.BackButton.hide()
  }, [tg, visible])
}

/** Ask before closing the Mini App while there is unsent text. */
export function useClosingConfirmation(enabled: boolean) {
  const runtime = useRuntime()
  const tg = runtime.mode === 'telegram' && supports(runtime.tg, 'closingConfirmation') ? runtime.tg : undefined
  useEffect(() => {
    if (!tg || !enabled) return
    tg.enableClosingConfirmation()
    return () => tg.disableClosingConfirmation()
  }, [tg, enabled])
}

/** Keep swipe-to-close from fighting a scrollable timeline. */
export function useDisableVerticalSwipes() {
  const runtime = useRuntime()
  const tg = runtime.mode === 'telegram' && supports(runtime.tg, 'verticalSwipes') ? runtime.tg : undefined
  useEffect(() => {
    if (!tg) return
    tg.disableVerticalSwipes()
    return () => tg.enableVerticalSwipes()
  }, [tg])
}
