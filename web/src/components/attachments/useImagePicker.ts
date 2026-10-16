import { useCallback, useEffect, useRef, useState } from 'react'
import { addFiles, filesFrom, type Picked } from '@/lib/attachments'

/**
 * Images picked for a task or message: from the file input (camera or
 * gallery on phones), pasted from the clipboard or dropped on the form.
 * Owns the preview object URLs and revokes them when an image leaves.
 */
export function useImagePicker() {
  // The list lives in a ref that only event handlers change (they also make
  // and revoke the preview URLs); the state copy re-renders.
  const list = useRef<Picked[]>([])
  const [picked, setPicked] = useState<Picked[]>([])
  const [errors, setErrors] = useState<string[]>([])
  const [dragging, setDragging] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    const ref = list
    return () => ref.current.forEach((p) => URL.revokeObjectURL(p.url))
  }, [])

  const set = useCallback((next: Picked[], errs: string[]) => {
    list.current = next
    setPicked(next)
    setErrors(errs)
  }, [])

  const add = useCallback(
    (files: File[]) => {
      if (files.length === 0) return
      const { next, errors } = addFiles(list.current, files, (f) => URL.createObjectURL(f))
      set(next, errors)
    },
    [set],
  )

  const remove = useCallback(
    (id: string) => {
      const gone = list.current.find((p) => p.id === id)
      if (gone) URL.revokeObjectURL(gone.url)
      set(
        list.current.filter((p) => p.id !== id),
        [],
      )
    },
    [set],
  )

  const clear = useCallback(() => {
    list.current.forEach((p) => URL.revokeObjectURL(p.url))
    set([], [])
  }, [set])

  const inputProps = {
    ref: inputRef,
    type: 'file' as const,
    multiple: true,
    hidden: true,
    onChange: (e: React.ChangeEvent<HTMLInputElement>) => {
      add(Array.from(e.target.files ?? []))
      // Picking the same file again must fire change again.
      e.target.value = ''
    },
  }

  /** Paste handler for a text field: takes images, leaves text pastes alone. */
  const onPaste = (e: React.ClipboardEvent) => {
    const files = filesFrom(e.clipboardData).filter((f) => f.type.startsWith('image/'))
    if (files.length === 0) return
    e.preventDefault()
    add(files)
  }

  /** Drag-and-drop on a container (desktop). */
  const dropProps = {
    onDragOver: (e: React.DragEvent) => {
      if (!Array.from(e.dataTransfer.types).includes('Files')) return
      e.preventDefault()
      setDragging(true)
    },
    onDragLeave: (e: React.DragEvent) => {
      if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDragging(false)
    },
    onDrop: (e: React.DragEvent) => {
      if (!Array.from(e.dataTransfer.types).includes('Files')) return
      e.preventDefault()
      setDragging(false)
      add(filesFrom(e.dataTransfer))
    },
  }

  return {
    picked,
    files: picked.map((p) => p.file),
    errors,
    dragging,
    add,
    remove,
    clear,
    open: () => inputRef.current?.click(),
    inputProps,
    onPaste,
    dropProps,
  }
}

export type ImagePicker = ReturnType<typeof useImagePicker>
