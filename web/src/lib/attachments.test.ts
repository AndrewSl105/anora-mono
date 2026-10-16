import { describe, expect, it } from 'vitest'
import { addFiles, MAX_FILE_BYTES, MAX_FILES } from './attachments'

const file = (name: string, type: string, size = 10) => new File([new Uint8Array(size)], name, { type })
const url = (f: File) => `blob:${f.name}`

describe('addFiles', () => {
  it('accepts PNG, JPEG, WebP and GIF and makes previews', () => {
    const { next, errors } = addFiles([], [file('a.png', 'image/png'), file('b.jpg', 'image/jpeg'), file('c.webp', 'image/webp'), file('d.gif', 'image/gif')], url)
    expect(errors).toEqual([])
    expect(next.map((p) => p.url)).toEqual(['blob:a.png', 'blob:b.jpg', 'blob:c.webp', 'blob:d.gif'])
  })

  it('names what it refuses and keeps the rest', () => {
    const { next, errors } = addFiles(
      [],
      [file('notes.pdf', 'application/pdf'), file('ok.png', 'image/png'), file('photo.heic', 'image/heic'), file('huge.png', 'image/png', MAX_FILE_BYTES + 1)],
      url,
    )
    expect(next.map((p) => p.file.name)).toEqual(['ok.png'])
    expect(errors).toEqual([
      'notes.pdf: only PNG, JPEG, WebP and GIF images can be attached',
      'photo.heic: only PNG, JPEG, WebP and GIF images can be attached',
      'huge.png is larger than 40 MB',
    ])
  })

  it('stops at 10 images and at 100 MB together', () => {
    const many = Array.from({ length: MAX_FILES + 2 }, (_, i) => file(`${i}.png`, 'image/png'))
    const r1 = addFiles([], many, url)
    expect(r1.next).toHaveLength(MAX_FILES)
    expect(r1.errors).toEqual(['At most 10 images per message'])

    const big = Array.from({ length: 3 }, (_, i) => file(`${i}.png`, 'image/png', 39 * 1024 * 1024))
    const r2 = addFiles([], big, url)
    expect(r2.next).toHaveLength(2)
    expect(r2.errors).toEqual(['Images are larger than 100 MB together'])
  })
})
