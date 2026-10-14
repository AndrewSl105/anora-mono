import { cn } from '@/lib/utils'

export function Logo({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 48 48" className={cn('text-primary', className)} aria-hidden="true">
      <rect width="48" height="48" rx="12" fill="currentColor" />
      <path d="M14 34 24 13l10 21M18.5 26h11" fill="none" stroke="var(--primary-foreground)" strokeWidth="3.5" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}
