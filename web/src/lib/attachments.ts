import { t } from '@/i18n/en'

// Server limits (API.md, "Attachments"); the server checks them again.
export const MAX_FILES = 10
export const MAX_FILE_BYTES = 40 * 1024 * 1024
export const MAX_TOTAL_BYTES = 100 * 1024 * 1024
export const IMAGE_TYPES = ['image/png', 'image/jpeg', 'image/webp', 'image/gif']
/** For <input accept>: explicit types make iOS hand over HEIC photos as JPEG. */
export const ACCEPT = IMAGE_TYPES.join(',')

export type Picked = { id: string; file: File; url: string }

let seq = 0

/**
 * Adds files to the picked list, skipping what the server would reject.
 * Returns the new list and one message per rejected file (or limit hit).
 */
export function addFiles(current: Picked[], files: File[], makeUrl: (f: File) => string): { next: Picked[]; errors: string[] } {
  const next = [...current]
  const errors: string[] = []
  let total = current.reduce((n, p) => n + p.file.size, 0)
  for (const file of files) {
    const name = file.name || t.attachments.pasted
    if (!IMAGE_TYPES.includes(file.type)) {
      errors.push(t.attachments.badType(name))
      continue
    }
    if (file.size > MAX_FILE_BYTES) {
      errors.push(t.attachments.tooBig(name))
      continue
    }
    if (next.length >= MAX_FILES) {
      errors.push(t.attachments.tooMany(MAX_FILES))
      break
    }
    if (total + file.size > MAX_TOTAL_BYTES) {
      errors.push(t.attachments.totalTooBig)
      break
    }
    total += file.size
    next.push({ id: `p${++seq}`, file, url: makeUrl(file) })
  }
  return { next, errors }
}

/** Image files from a paste or drop (DataTransfer), in order. */
export function filesFrom(data: DataTransfer | null): File[] {
  if (!data) return []
  const out: File[] = []
  for (const item of Array.from(data.items ?? [])) {
    if (item.kind === 'file') {
      const f = item.getAsFile()
      if (f) out.push(f)
    }
  }
  return out.length ? out : Array.from(data.files ?? [])
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}
