import { useRef, useState, type RefObject, type TouchEvent } from 'react'
import { haptic } from '@/telegram/haptics'

const THRESHOLD = 50
const MAX = 100

/** Touch pull-down at the top of a scroll container refreshes it. */
export function usePullToRefresh(ref: RefObject<HTMLElement | null>, onRefresh: () => unknown) {
  const start = useRef<number | null>(null)
  const [distance, setDistance] = useState(0)

  const handlers = {
    onTouchStart(e: TouchEvent) {
      start.current = (ref.current?.scrollTop ?? 0) <= 0 ? e.touches[0].clientY : null
    },
    onTouchMove(e: TouchEvent) {
      if (start.current === null) return
      const dy = e.touches[0].clientY - start.current
      setDistance(dy > 0 ? Math.min(MAX, dy * 0.5) : 0)
    },
    onTouchEnd() {
      if (distance >= THRESHOLD) {
        haptic.tap()
        void onRefresh()
      }
      start.current = null
      setDistance(0)
    },
  }
  return { handlers, distance, ready: distance >= THRESHOLD }
}
