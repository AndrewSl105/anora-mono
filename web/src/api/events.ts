import type { ApiEvent } from './types'

// Event types come from the generated OpenAPI `Event` union (one schema per
// event type, discriminated by `type`), so `switch (event.type)` narrows the payload.
export type EventType = ApiEvent['type']
export type EventOf<K extends EventType> = Extract<ApiEvent, { type: K }>
export type PayloadOf<K extends EventType> = EventOf<K>['payload']

/** Full question id as the backend stores it: "<run_id>-<runner question id>". */
export function fullQuestionId(runId: number | null | undefined, questionId: string): string {
  if (runId == null || questionId.includes('-')) return questionId
  return `${runId}-${questionId}`
}
