// Maps Cursor SDK run messages to runner events. Pure apart from its own state
// (agent id, open tool calls, last reply, summed usage), so it is
// unit-testable with synthetic messages.
import { isAbsolute, relative } from "node:path";
import type { SDKMessage } from "@cursor/sdk";
import { DONE_SUMMARY_MAX, TEXT_MAX, truncate } from "./mapper.ts";
import type { EventBody } from "./protocol.ts";

const SUMMARY_MAX = 80;
const RESULT_SUMMARY_MAX = 120;

// Cursor's public tool names, shown as the Claude tool the timeline knows.
const TOOL_NAMES: Record<string, string> = {
  shell: "Bash",
  read: "Read",
  write: "Write",
  edit: "Edit",
  delete: "Delete",
  grep: "Grep",
  glob: "Glob",
  ls: "LS",
  webSearch: "WebSearch",
  webFetch: "WebFetch",
  updateTodos: "TodoWrite",
  task: "Task",
};

function rel(p: unknown, workspace: string): string {
  if (typeof p !== "string") return "";
  if (!isAbsolute(p)) return p;
  const r = relative(workspace, p);
  return r.startsWith("..") ? p : r || ".";
}

export function cursorToolSummary(name: string, args: unknown, workspace: string): string {
  const a = (args ?? {}) as Record<string, unknown>;
  const tool = TOOL_NAMES[name] ?? name;
  let s: string;
  switch (name) {
    case "shell":
      s = `Bash: ${String(a.command ?? "").split("\n")[0]}`;
      break;
    case "read":
    case "write":
    case "edit":
    case "delete":
    case "ls":
      s = `${tool} ${rel(a.path, workspace)}`;
      break;
    case "grep":
      s = `Grep ${String(a.pattern ?? "")}${a.path ? ` in ${rel(a.path, workspace)}` : ""}`;
      break;
    case "glob":
      s = `Glob ${String(a.globPattern ?? "")}`;
      break;
    case "mcp":
      s = `${String(a.providerIdentifier ?? "mcp")}.${String(a.toolName ?? "")}`;
      break;
    default:
      s = tool;
  }
  return truncate(s, SUMMARY_MAX);
}

// toolFailure returns a one-line reason when a tool result is a failure, or
// null when it succeeded. Results are `{status: "success", value}` or
// `{status: "error", error: {message}}`; a shell success can still exit non-zero.
export function toolFailure(name: string, result: unknown): string | null {
  const r = (result ?? {}) as { status?: string; value?: Record<string, unknown>; error?: { message?: string } };
  if (r.status === "error") return r.error?.message ? truncate(r.error.message, RESULT_SUMMARY_MAX) : "";
  if (name === "shell" && r.value && typeof r.value.exitCode === "number" && r.value.exitCode !== 0) {
    const text = String(r.value.stderr || r.value.stdout || "");
    const line = text.split("\n").find((l) => l.trim() !== "");
    return line ? truncate(line, RESULT_SUMMARY_MAX) : `exit ${r.value.exitCode}`;
  }
  return null;
}

export class CursorMapper {
  sessionId = "";
  lastText = "";
  failure = "";
  input = 0;
  output = 0;
  turns = 0;

  private readonly workspace: string;
  private readonly model: string;
  private started = false;
  private readonly open = new Set<string>();

  constructor(workspace: string, model: string) {
    this.workspace = workspace;
    this.model = model;
  }

  start(agentId: string): EventBody[] {
    this.sessionId = agentId;
    if (this.started) return [];
    this.started = true;
    return [{ type: "started", session_id: agentId, model: this.model }];
  }

  get summary(): string {
    const t = this.lastText.trim();
    return t.length <= DONE_SUMMARY_MAX ? t : t.slice(0, DONE_SUMMARY_MAX - 1) + "…";
  }

  map(msg: SDKMessage): EventBody[] {
    switch (msg.type) {
      case "assistant": {
        // Tool calls arrive as `tool_call` messages; only the text is used here.
        const out: EventBody[] = [];
        for (const block of msg.message.content) {
          if (block.type !== "text" || !block.text.trim()) continue;
          this.lastText = block.text;
          out.push({ type: "assistant_text", text: block.text.length > TEXT_MAX ? block.text.slice(0, TEXT_MAX) + "…" : block.text });
        }
        return out;
      }
      case "tool_call": {
        const tool = TOOL_NAMES[msg.name] ?? msg.name;
        const out: EventBody[] = [];
        if (!this.open.has(msg.call_id)) {
          this.open.add(msg.call_id);
          out.push({ type: "tool_use", tool, summary: cursorToolSummary(msg.name, msg.args, this.workspace) });
        }
        if (msg.status === "running") return out;
        const failure = msg.status === "error" ? (toolFailure(msg.name, msg.result) ?? "") : toolFailure(msg.name, msg.result);
        if (failure === null) out.push({ type: "tool_result", tool, ok: true });
        else out.push(failure ? { type: "tool_result", tool, ok: false, summary: failure } : { type: "tool_result", tool, ok: false });
        // A later message for the same call (e.g. a retry) starts a new pair.
        this.open.delete(msg.call_id);
        return out;
      }
      case "usage":
        this.turns++;
        this.input += msg.usage.inputTokens + msg.usage.cacheReadTokens + msg.usage.cacheWriteTokens;
        this.output += msg.usage.outputTokens;
        return [{ type: "usage", cost_usd_total: 0, input_tokens: this.input, output_tokens: this.output, turns: this.turns }];
      case "status":
        if (msg.status === "ERROR" || msg.status === "EXPIRED") this.failure = msg.message || msg.status.toLowerCase();
        return [];
      default:
        return [];
    }
  }
}
