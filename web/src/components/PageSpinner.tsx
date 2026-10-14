export function PageSpinner() {
  return (
    <div className="flex h-full items-center justify-center" role="status" aria-label="Loading">
      <div className="size-6 animate-spin rounded-full border-2 border-muted-foreground/25 border-t-primary" />
    </div>
  )
}
