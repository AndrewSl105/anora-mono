import type { components, paths } from './schema'

type S = components['schemas']

export type Task = S['Task']
export type TaskView = S['TaskView']
export type TaskStatus = S['TaskStatus']
export type Project = S['Project']
export type ProjectFields = S['ProjectFields']
export type Repository = S['Repository']
export type GitHubRepo = S['GitHubRepo']
export type Question = S['Question']
export type Run = S['Run']
export type ApiEvent = S['Event']
export type Attachment = S['Attachment']
export type SessionInfo = S['SessionInfo']
export type Agent = S['Agent']
export type HostNode = S['Node']
export type Stats = paths['/api/v1/stats']['get']['responses'][200]['content']['application/json']
export type Diff = paths['/api/v1/tasks/{id}/diff']['get']['responses'][200]['content']['application/json']
export type Health = components['responses']['Health']['content']['application/json']

export type Page<T> = { items: T[]; next_cursor: string }
