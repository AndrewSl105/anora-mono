import { useEffect, useMemo, useState } from 'react'
import { useNavigate, Link } from 'react-router'
import { useForm, Controller } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { errorMessage, useCreateTask, useProjects } from '@/api/hooks'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { PageSpinner } from '@/components/PageSpinner'
import { PrimaryAction, useClosingConfirmation } from '@/telegram/controls'
import { useImagePicker } from '@/components/attachments/useImagePicker'
import { AttachButton, DropOverlay, PickedImages } from '@/components/attachments/PickedImages'
import { haptic } from '@/telegram/haptics'
import { storage } from '@/lib/storage'
import { t } from '@/i18n/en'

const LAST_PROJECT = 'fleetpocket.lastProject'
const draftKey = (project: string) => `fleetpocket.draft.${project}`

const schema = z.object({
  project_id: z.string().min(1, t.newTask.projectRequired),
  repository_id: z.string().optional(),
  title: z.string().max(200).optional(),
  prompt: z.string().trim().min(1, t.newTask.promptRequired),
})
type Values = z.infer<typeof schema>

/** Request body with blank optional fields omitted (the backend rejects unknown/empty values). */
export function toCreateBody(v: Values) {
  return {
    project_id: v.project_id,
    prompt: v.prompt.trim(),
    ...(v.repository_id ? { repository_id: Number(v.repository_id) } : {}),
    ...(v.title?.trim() ? { title: v.title.trim() } : {}),
  }
}

export default function NewTaskPage() {
  const projects = useProjects()
  const create = useCreateTask()
  const navigate = useNavigate()

  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { project_id: '', repository_id: '', title: '', prompt: '' },
  })
  const projectId = form.watch('project_id')
  const prompt = form.watch('prompt')
  const project = useMemo(() => projects.data?.find((p) => p.id === projectId), [projects.data, projectId])
  const picker = useImagePicker()
  const [progress, setProgress] = useState<number | null>(null)
  useClosingConfirmation(prompt.trim().length > 0 || picker.picked.length > 0)

  // Pre-select the last used project once projects load.
  useEffect(() => {
    if (!projects.data?.length || form.getValues('project_id')) return
    const last = storage.get(LAST_PROJECT)
    const pick = projects.data.find((p) => p.id === last) ?? projects.data[0]
    form.setValue('project_id', pick.id)
  }, [projects.data, form])

  // Draft per project; reset the repo choice when the project changes.
  useEffect(() => {
    if (!projectId) return
    form.setValue('prompt', storage.get(draftKey(projectId)) ?? '')
    const def = project?.repositories.find((r) => r.is_default) ?? project?.repositories[0]
    form.setValue('repository_id', def ? String(def.id) : '')
  }, [projectId, project, form])

  useEffect(() => {
    if (projectId) storage.set(draftKey(projectId), prompt || null)
  }, [projectId, prompt])

  const submit = form.handleSubmit(async (values) => {
    try {
      const files = picker.files
      if (files.length) setProgress(0)
      const task = await create.mutateAsync({
        ...toCreateBody(values),
        files,
        onProgress: files.length ? setProgress : undefined,
      })
      picker.clear()
      haptic.success()
      storage.set(LAST_PROJECT, values.project_id)
      storage.set(draftKey(values.project_id), null)
      toast.success(t.newTask.created)
      void navigate(`/tasks/${task.id}`, { replace: true })
    } catch (e) {
      haptic.error()
      toast.error(errorMessage(e))
    } finally {
      setProgress(null)
    }
  })

  if (projects.isPending) return <PageSpinner />
  if (!projects.data?.length) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center">
        <p className="text-muted-foreground">{t.newTask.noProjects}</p>
        <Button asChild className="h-11">
          <Link to="/projects/new">{t.projects.new}</Link>
        </Button>
      </div>
    )
  }

  const repos = project?.repositories ?? []
  const errors = form.formState.errors

  return (
    <div className="h-full overflow-y-auto">
      <form
        onSubmit={(e) => void submit(e)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
            e.preventDefault()
            void submit()
          }
        }}
        className="relative mx-auto max-w-2xl space-y-5 px-4 pt-4 pb-6"
        {...picker.dropProps}
      >
        <DropOverlay show={picker.dragging} />
        <h1 className="text-xl font-semibold">{t.newTask.title}</h1>

        <div className="grid gap-5 sm:grid-cols-2">
          <div className="space-y-2">
            <Label htmlFor="project">{t.newTask.project}</Label>
            <Controller
              control={form.control}
              name="project_id"
              render={({ field }) => (
                <Select value={field.value} onValueChange={field.onChange}>
                  <SelectTrigger id="project" className="h-11 w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {projects.data.map((p) => (
                      <SelectItem key={p.id} value={p.id} className="h-10">
                        {p.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            />
            {errors.project_id && <p className="text-xs text-destructive">{errors.project_id.message}</p>}
          </div>

          {repos.length > 1 && (
            <div className="space-y-2">
              <Label htmlFor="repo">{t.newTask.repository}</Label>
              <Controller
                control={form.control}
                name="repository_id"
                render={({ field }) => (
                  <Select value={field.value} onValueChange={field.onChange}>
                    <SelectTrigger id="repo" className="h-11 w-full font-mono text-sm">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {repos.map((r) => (
                        <SelectItem key={r.id} value={String(r.id)} className="h-10 font-mono text-sm">
                          {r.full_name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              />
            </div>
          )}
        </div>

        <div className="space-y-2">
          <Label htmlFor="prompt">{t.newTask.prompt}</Label>
          <Textarea
            id="prompt"
            {...form.register('prompt')}
            placeholder={t.newTask.promptPlaceholder}
            className="min-h-40 field-sizing-content"
            aria-invalid={!!errors.prompt}
            onPaste={picker.onPaste}
          />
          {errors.prompt && <p className="text-xs text-destructive">{errors.prompt.message}</p>}
          <div className="flex items-start gap-2">
            <AttachButton picker={picker} disabled={create.isPending} className="-ml-2" />
            <div className="min-w-0 flex-1 pt-1">
              <PickedImages picker={picker} progress={progress} />
            </div>
          </div>
        </div>

        <div className="space-y-2">
          <Label htmlFor="title">{t.newTask.titleLabel}</Label>
          <Input id="title" {...form.register('title')} placeholder={t.newTask.titlePlaceholder} className="h-11" />
        </div>

        <PrimaryAction label={t.newTask.start} onClick={() => void submit()} loading={create.isPending} />
      </form>
    </div>
  )
}
