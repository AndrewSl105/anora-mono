import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { setupServer } from 'msw/node'
import { http, HttpResponse } from 'msw'
import type { ReactNode } from 'react'
import { RuntimeProvider } from '@/telegram/runtime'
import { setBearerToken, uploadForm } from '@/api/client'
import type { Attachment, TaskView } from '@/api/types'
import { Composer } from '@/routes/tasks/Composer'
import { AttachmentGrid } from './AttachmentGrid'

// MSW's XMLHttpRequest interceptor cannot read a jsdom FormData body, so the
// multipart transport is replaced by a recorder; the JSON and blob calls go
// through the real client and MSW.
vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/client')>()),
  uploadForm: vi.fn(),
}))

const server = setupServer()
beforeAll(() => {
  server.listen({ onUnhandledFrame: 'error' })
  // jsdom has no object URLs.
  let n = 0
  vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: () => `blob:test/${++n}`, revokeObjectURL: () => {} }))
})
afterEach(() => {
  server.resetHandlers()
  setBearerToken(null)
})
afterAll(() => server.close())

function wrap(children: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <RuntimeProvider runtime={{ mode: 'browser' }}>
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    </RuntimeProvider>,
  )
}

const png = (name: string) => new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], name, { type: 'image/png' })

describe('composer attachments', () => {
  const task = { id: 7, status: 'pr_open', queued_message: null } as unknown as TaskView

  it('previews picked images, explains refused files and uploads with the text', async () => {
    const upload = vi.mocked(uploadForm)
    upload.mockImplementation(async (_path, _form, onProgress) => {
      onProgress?.(0.5)
      return { ...task, status: 'queued' }
    })
    const user = userEvent.setup({ applyAccept: false })
    wrap(<Composer task={task} />)

    await user.upload(screen.getByTestId('attach-input'), [png('mock.png'), new File(['%PDF'], 'spec.pdf', { type: 'application/pdf' })])
    expect(screen.getByAltText('mock.png')).toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent('spec.pdf: only PNG, JPEG, WebP and GIF images can be attached')

    // Text is required; the images alone cannot be sent.
    const sendButton = screen.getByRole('button', { name: 'Send' })
    expect(sendButton).toBeDisabled()
    await user.type(screen.getByRole('textbox'), 'Make the header like this')
    await user.click(sendButton)

    await waitFor(() => expect(upload).toHaveBeenCalledTimes(1))
    const [path, form] = upload.mock.calls[0]
    expect(path).toBe('/api/v1/tasks/7/message')
    expect(form.get('text')).toBe('Make the header like this')
    expect((form.getAll('files[]') as File[]).map((f) => f.name)).toEqual(['mock.png'])
    // Sent: the previews are cleared.
    await waitFor(() => expect(screen.queryByAltText('mock.png')).not.toBeInTheDocument())
  })

  it('removes a picked image', async () => {
    const user = userEvent.setup()
    wrap(<Composer task={task} />)
    await user.upload(screen.getByTestId('attach-input'), [png('a.png'), png('b.png')])
    await user.click(screen.getByRole('button', { name: 'Remove a.png' }))
    expect(screen.queryByAltText('a.png')).not.toBeInTheDocument()
    expect(screen.getByAltText('b.png')).toBeInTheDocument()
  })
})

describe('timeline thumbnails', () => {
  const live: Attachment = { id: 1, name: 'mock.png', mime: 'image/png', size: 4, width: 800, height: 400 }
  const gone: Attachment = { id: 2, name: 'old.png', mime: 'image/png', size: 4, width: 10, height: 10 }

  it('loads thumbnails with the bearer token and shows removed images as removed', async () => {
    const auth: string[] = []
    server.use(
      http.get('*/api/v1/tasks/3/attachments/:aid', ({ request, params }) => {
        auth.push(request.headers.get('Authorization') ?? '')
        expect(new URL(request.url).searchParams.get('thumb')).toBe('1')
        if (params.aid === '2') return HttpResponse.json({ error: { code: 'gone', message: 'removed' } }, { status: 410 })
        return new HttpResponse(new Uint8Array([1, 2, 3, 4]), { headers: { 'Content-Type': 'image/jpeg' } })
      }),
    )
    setBearerToken('tg-token')
    wrap(<AttachmentGrid taskId={3} items={[live, gone]} />)

    expect(await screen.findByText('Image removed')).toBeInTheDocument()
    const thumb = await screen.findByAltText('mock.png')
    await waitFor(() => expect(thumb.getAttribute('src')).toMatch(/^blob:/))
    expect(auth).toEqual(['Bearer tg-token', 'Bearer tg-token'])
    // The thumbnail box keeps the image's aspect ratio (2:1).
    const button = screen.getByRole('button', { name: 'Open mock.png' })
    expect(button.style.width).toBe('112px')
    expect(button.style.height).toBe('56px')
  })

  it('opens the full image on tap', async () => {
    const asked: string[] = []
    server.use(
      http.get('*/api/v1/tasks/3/attachments/1', ({ request }) => {
        asked.push(new URL(request.url).search)
        return new HttpResponse(new Uint8Array([1]), { headers: { 'Content-Type': 'image/png' } })
      }),
    )
    const user = userEvent.setup()
    wrap(<AttachmentGrid taskId={3} items={[live]} />)
    const button = await screen.findByRole('button', { name: 'Open mock.png' })
    await waitFor(() => expect(button).toBeEnabled())
    await user.click(button)
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
    await waitFor(() => expect(asked).toEqual(['?thumb=1', '']))
  })
})
