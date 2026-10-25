// JSON Lines protocol between the Fleetpocket server (Go) and the runner.
// Mirrored in internal/runnermgr/protocol.go; documented in docs/PROTOCOL.md.
import { z } from "zod";

export const RUNNER_VERSION = "0.1.0";

export const ExitCode = {
  Done: 0,
  Error: 1,
  Cancelled: 2,
  Paused: 3,
  LimitExceeded: 4,
} as const;

// ---- Commands (stdin) ----

export const RunCommand = z.object({
  type: z.literal("run"),
  task_id: z.union([z.string(), z.number()]).transform(String),
  prompt: z.string().min(1),
  append_system_prompt: z.string().optional(),
  resume_session_id: z.string().optional(),
  model: z.string().optional(),
  limits: z.object({
    max_turns: z.number().int().positive(),
    max_budget_usd: z.number().positive(),
    max_duration_sec: z.number().int().positive(),
    question_timeout_sec: z.number().int().positive(),
  }),
  commit: z.object({
    message: z.string().min(1),
    author_name: z.string().min(1),
    author_email: z.string().min(1),
  }),
});

export const AnswerCommand = z.object({
  type: z.literal("answer"),
  question_id: z.string().min(1),
  text: z.string(),
});

export const MessageCommand = z.object({
  type: z.literal("message"),
  text: z.string().min(1),
});

export const InterruptCommand = z.object({ type: z.literal("interrupt") });
export const CancelCommand = z.object({ type: z.literal("cancel") });

export const Command = z.discriminatedUnion("type", [
  RunCommand,
  AnswerCommand,
  MessageCommand,
  InterruptCommand,
  CancelCommand,
]);

export type RunCommand = z.infer<typeof RunCommand>;
export type Command = z.infer<typeof Command>;

export function parseCommand(line: string): { ok: true; command: Command } | { ok: false; error: string } {
  let raw: unknown;
  try {
    raw = JSON.parse(line);
  } catch {
    return { ok: false, error: "invalid JSON" };
  }
  const res = Command.safeParse(raw);
  if (!res.success) return { ok: false, error: res.error.issues.map((i) => `${i.path.join(".") || "(root)"}: ${i.message}`).join("; ") };
  return { ok: true, command: res.data };
}

// ---- Events (stdout) ----
// Every event also carries `ts` (RFC 3339) and `seq` (monotonic int), added by the emitter.

export type EventBody =
  | { type: "ready"; runner_version: string }
  | { type: "started"; session_id: string; model: string }
  | { type: "assistant_text"; text: string }
  | { type: "tool_use"; tool: string; summary: string }
  | { type: "tool_result"; tool: string; ok: boolean; summary?: string }
  | { type: "question"; question_id: string; text: string; options?: string[] }
  | { type: "usage"; cost_usd_total: number; input_tokens: number; output_tokens: number; turns: number }
  | { type: "committed"; sha: string; files_changed: number; insertions: number; deletions: number }
  | { type: "done"; session_id: string; summary: string; no_changes: boolean; cost_usd_total: number }
  | { type: "paused"; session_id: string; reason: "question_timeout"; question_id: string }
  | { type: "error"; code: string; message: string; session_id?: string }
  | { type: "heartbeat" };

export type RunnerEvent = EventBody & { ts: string; seq: number };

export type EventType = EventBody["type"];
