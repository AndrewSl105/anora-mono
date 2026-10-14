import { useState, type ReactNode } from 'react'
import { useNavigate, useParams } from 'react-router'
import { useFieldArray, useForm, Controller } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { toast } from 'sonner'
import { Archive, Plus, Star, Trash2, X } from 'lucide-react'
import {
  errorMessage,
  useAddRepository,
  useArchiveProject,
  useCreateProject,
  useGitHubRepos,
  useProject,
  useRemoveRepository,
  useUpdateProject,
} from '@/api/hooks'
import { isApiError } from '@/api/errors'
import type { Project } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { PageSpinner } from '@/components/PageSpinner'
import { useConfirm } from '@/components/confirm'
import { NotFound } from '@/routes/NotFound'
import { PrimaryAction } from '@/telegram/controls'
import { haptic } from '@/telegram/haptics'
import { t } from '@/i18n/en'
import { createBody, patchBody, projectSchema, toFormValues, type ProjectFormValues } from './projectForm'

const f = t.projects.fields

function Field({ id, label, hint, error, children }: { id: string; label: string; hint?: string; error?: string; children: ReactNode }) {
  return (
    <div className="space-y-2">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {hint && !error && <p className="text-xs text-muted-foreground">{hint}</p>}
      {error && <p className="text-xs text-destructive">{error}</p>}
    </div>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="space-y-4">
      <h2 className="text-sm font-semibold tracking-wide text-muted-foreground uppercase">{title}</h2>
      {children}
    </section>
  )
}

export default function ProjectEditPage() {
  const { id } = useParams()
  const project = useProject(id)
  if (!id) return <ProjectForm />
  if (project.isPending) return <PageSpinner />
  if (project.isError) return <NotFound title={isApiError(project.error, 'not_found') ? t.system.notFound : t.common.somethingWrong} />
  return <ProjectForm key={project.data.updated_at} project={project.data} />
}

function ProjectForm({ project }: { project?: Project }) {
  const navigate = useNavigate()
  const confirm = useConfirm()
  const creating = !project
  const create = useCreateProject()
  const update = useUpdateProject(project?.id ?? '')
  const archive = useArchiveProject(project?.id ?? '')

  const form = useForm<ProjectFormValues>({
    resolver: zodResolver(projectSchema),
    defaultValues: toFormValues(project),
  })
  const env = useFieldArray({ control: form.control, name: 'env' })
  const errors = form.formState.errors
  const busy = create.isPending || update.isPending

  const submit = form.handleSubmit(async (values) => {
    try {
      if (creating) {
        const p = await create.mutateAsync(createBody(values))
        toast.success(t.projects.created)
        void navigate(`/projects/${p.id}`, { replace: true })
      } else {
        const body = patchBody(values, project)
        if (Object.keys(body).length) await update.mutateAsync(body)
        toast.success(t.projects.saved)
      }
      haptic.success()
    } catch (e) {
      haptic.error()
      toast.error(errorMessage(e))
    }
  })

  async function onArchive() {
    if (!(await confirm(t.projects.confirmArchive, t.projects.archive))) return
    await archive.mutateAsync()
    toast.success(t.projects.archived)
    void navigate('/projects', { replace: true })
  }

  const num = (name: 'max_turns' | 'max_budget_usd' | 'max_duration_sec' | 'runner_cpus', label: string, step = '1') => (
    <Field id={name} label={label} error={errors[name]?.message}>
      <Input id={name} inputMode="decimal" step={step} placeholder={t.common.serverDefault} className="h-11" {...form.register(name)} />
    </Field>
  )

  return (
    <div className="h-full overflow-y-auto">
      <form onSubmit={(e) => void submit(e)} className="mx-auto max-w-2xl space-y-8 px-4 pt-4 pb-8">
        <h1 className="text-xl font-semibold">{creating ? t.projects.new : project.name}</h1>

        <Section title="General">
          {creating && (
            <Field id="id" label={f.id} error={errors.id?.message}>
              <Input id="id" autoCapitalize="none" className="h-11 font-mono" {...form.register('id')} />
            </Field>
          )}
          <Field id="name" label={f.name} error={errors.name?.message}>
            <Input id="name" className="h-11" {...form.register('name')} />
          </Field>
          <Field id="instructions" label={f.instructions} hint={f.instructionsHint}>
            <Textarea id="instructions" className="min-h-28 field-sizing-content" {...form.register('instructions')} />
          </Field>
          <Field id="model" label={f.model}>
            <Input id="model" placeholder={t.common.serverDefault} className="h-11 font-mono" {...form.register('model')} />
          </Field>
        </Section>

        <Section title={f.services}>
          {(['postgres', 'redis'] as const).map((s) => (
            <Controller
              key={s}
              control={form.control}
              name={s}
              render={({ field }) => (
                <label className="flex min-h-11 items-center justify-between gap-3 rounded-lg border px-3">
                  <span className="text-sm font-medium capitalize">{s === 'postgres' ? 'PostgreSQL' : 'Redis'}</span>
                  <Switch checked={field.value} onCheckedChange={field.onChange} />
                </label>
              )}
            />
          ))}
        </Section>

        <Section title={f.env}>
          <p className="-mt-2 text-xs text-muted-foreground">{f.envHint}</p>
          {env.fields.map((row, i) => (
            <div key={row.id} className="flex items-start gap-2">
              <div className="w-2/5 min-w-0">
                <Input
                  aria-label="Key"
                  placeholder="KEY"
                  autoCapitalize="characters"
                  className="h-11 font-mono"
                  {...form.register(`env.${i}.key`)}
                />
                {errors.env?.[i]?.key && <p className="mt-1 text-xs text-destructive">{errors.env[i]?.key?.message}</p>}
              </div>
              <Input aria-label="Value" placeholder="value" className="h-11 min-w-0 flex-1 font-mono" {...form.register(`env.${i}.value`)} />
              <Button type="button" variant="ghost" size="icon" className="size-11" onClick={() => env.remove(i)} aria-label={t.common.remove}>
                <X />
              </Button>
            </div>
          ))}
          <Button type="button" variant="outline" className="h-10" onClick={() => env.append({ key: '', value: '' })}>
            <Plus /> {t.projects.addVar}
          </Button>
        </Section>

        <Section title={f.limits}>
          <div className="grid grid-cols-2 gap-4">
            {num('max_turns', f.maxTurns)}
            {num('max_budget_usd', f.maxBudget, '0.01')}
            {num('max_duration_sec', f.maxDuration)}
            {num('runner_cpus', f.cpus, '0.5')}
            <Field id="runner_memory" label={f.memory}>
              <Input id="runner_memory" placeholder={t.common.serverDefault} className="h-11 font-mono" {...form.register('runner_memory')} />
            </Field>
          </div>
        </Section>

        <PrimaryAction label={creating ? t.projects.new : t.common.save} onClick={() => void submit()} loading={busy} />

        {project && <Repositories project={project} />}

        {project && (
          <Button type="button" variant="outline" className="h-11 w-full text-destructive" onClick={() => void onArchive()} disabled={archive.isPending}>
            <Archive /> {t.projects.archive}
          </Button>
        )}
      </form>
    </div>
  )
}

function Repositories({ project }: { project: Project }) {
  const [adding, setAdding] = useState(false)
  const remove = useRemoveRepository(project.id)
  return (
    <Section title={f.repositories}>
      <ul className="divide-y rounded-xl border">
        {project.repositories.map((r) => (
          <li key={r.id} className="flex min-h-14 items-center gap-3 px-3 py-2">
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-1.5 truncate font-mono text-sm">
                {r.full_name}
                {r.is_default && <Star className="size-3.5 shrink-0 fill-amber-400 text-amber-500" aria-label={f.isDefault} />}
              </div>
              <div className="text-xs text-muted-foreground">
                {f.baseBranch}: <span className="font-mono">{r.base_branch}</span>
              </div>
            </div>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="size-11"
              aria-label={`${t.common.remove} ${r.full_name}`}
              disabled={remove.isPending}
              onClick={() => remove.mutate(r.id, { onSuccess: () => toast.success(t.projects.repoRemoved) })}
            >
              <Trash2 />
            </Button>
          </li>
        ))}
      </ul>
      {adding ? (
        <AddRepository project={project} onDone={() => setAdding(false)} />
      ) : (
        <Button type="button" variant="outline" className="h-10" onClick={() => setAdding(true)}>
          <Plus /> {t.projects.addRepo}
        </Button>
      )}
    </Section>
  )
}

function AddRepository({ project, onDone }: { project: Project; onDone: () => void }) {
  const repos = useGitHubRepos(true)
  const add = useAddRepository(project.id)
  const [name, setName] = useState('')
  const [base, setBase] = useState('dev')
  const [isDefault, setIsDefault] = useState(project.repositories.length === 0)
  const existing = new Set(project.repositories.map((r) => r.full_name))
  const options = repos.data?.items.filter((r) => !existing.has(r.full_name)) ?? []
  const picked = options.find((r) => r.full_name === name)

  if (repos.isError) {
    return (
      <p className="rounded-lg border px-3 py-2 text-sm text-muted-foreground">
        {isApiError(repos.error, 'github_disabled') ? t.projects.githubDisabled : t.common.somethingWrong}
      </p>
    )
  }

  return (
    <div className="space-y-4 rounded-xl border p-3">
      <Field id="gh-repo" label={t.projects.pickRepo}>
        <Select value={name} onValueChange={setName} disabled={repos.isPending}>
          <SelectTrigger id="gh-repo" className="h-11 w-full font-mono text-sm">
            <SelectValue placeholder={repos.isPending ? t.common.loading : t.projects.pickRepo} />
          </SelectTrigger>
          <SelectContent>
            {options.map((r) => (
              <SelectItem key={r.full_name} value={r.full_name} className="h-10 font-mono text-sm">
                {r.full_name}
                {r.private ? ' 🔒' : ''}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      <Field id="base" label={f.baseBranch}>
        <Input id="base" value={base} onChange={(e) => setBase(e.target.value)} className="h-11 font-mono" />
      </Field>
      <label className="flex min-h-11 items-center justify-between gap-3">
        <span className="text-sm">{f.isDefault}</span>
        <Switch checked={isDefault} onCheckedChange={setIsDefault} />
      </label>
      <div className="flex gap-2">
        <Button type="button" variant="ghost" className="h-11" onClick={onDone}>
          {t.common.cancel}
        </Button>
        <Button
          type="button"
          className="h-11 flex-1"
          disabled={!picked || add.isPending}
          onClick={() =>
            picked &&
            add.mutate(
              {
                full_name: picked.full_name,
                installation_id: picked.installation_id,
                ...(base.trim() ? { base_branch: base.trim() } : {}),
                is_default: isDefault,
              },
              {
                onSuccess: () => {
                  toast.success(t.projects.repoAdded)
                  onDone()
                },
              },
            )
          }
        >
          {t.common.add}
        </Button>
      </div>
    </div>
  )
}
