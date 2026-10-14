import type { Control } from "./control.ts";
import type { Emitter } from "./emitter.ts";
import type { RunCommand } from "./protocol.ts";

export interface SessionContext {
  cmd: RunCommand;
  workspace: string;
  emitter: Emitter;
  control: Control;
}

export type SessionOutcome =
  | { kind: "done"; session_id: string; summary: string; cost_usd_total: number }
  | { kind: "paused"; session_id: string; question_id: string }
  | { kind: "limit"; session_id: string; message: string }
  | { kind: "cancelled"; session_id: string }
  | { kind: "error"; session_id?: string; code: string; message: string };

export type Session = (ctx: SessionContext) => Promise<SessionOutcome>;
