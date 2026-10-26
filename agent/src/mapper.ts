// Maps Agent SDK messages to runner events. Pure apart from its own state
// (tool_use_id → tool name, latest usage), so it is unit-testable with
// synthetic SDK messages.
import { isAbsolute, relative } from "node:path";
import type { SDKMessage, SDKResultMessage } from "@anthropic-ai/claude-agent-sdk";
import type { EventBody } from "./protocol.ts";

export const ASK_TOOL = "AskUserQuestion";
const SUMMARY_MAX = 80;
const RESULT_SUMMARY_MAX = 120;
export const TEXT_MAX = 4000;
export const DONE_SUMMARY_MAX = 2000;

export function truncate(s: string, max: number): string {
  const one = s.replace(/\s+/g, " ").trim();
  return one.length <= max ? one : one.slice(0, max - 1) + "…";
}

function rel(p: unknown, workspace: string): string {
  if (typeof p !== "string") return "";
  if (!isAbsolute(p)) return p;
  const r = relative(workspace, p);
  return r.startsWith("..") ? p : r || ".";
}

export function toolSummary(tool: string, input: unknown, workspace: string): string {
  const i = (input ?? {}) as Record<string, unknown>;
  let s: string;
  switch (tool) {
    case "Read":
    case "Write":
    case "Edit":
    case "MultiEdit":
      s = `${tool} ${rel(i.file_path, workspace)}`;
      break;
    case "NotebookEdit":
      s = `${tool} ${rel(i.notebook_path, workspace)}`;
      break;
    case "Bash":
      s = `Bash: ${String(i.command ?? "").split("\n")[0]}`;
      break;
    case "Glob":
      s = `Glob ${String(i.pattern ?? "")}`;
      break;
    case "Grep":
      s = `Grep ${String(i.pattern ?? "")}${i.path ? ` in ${rel(i.path, workspace)}` : ""}`;
      break;
    case "WebFetch":
      s = `WebFetch ${String(i.url ?? "")}`;
      break;
    case "WebSearch":
      s = `WebSearch ${String(i.query ?? "")}`;
      break;
    case "TodoWrite":
      s = `TodoWrite (${Array.isArray(i.todos) ? i.todos.length : 0} items)`;
      break;
    default:
      s = tool;
  }
  return truncate(s, SUMMARY_MAX);
}

// resultSummary returns a short, one-line summary of a failed tool result.
// Successful results get no summary: their content may be file contents.
function resultSummary(content: unknown): string | undefined {
  let text = "";
  if (typeof content === "string") text = content;
  else if (Array.isArray(content)) {
    const first = content.find((c) => c && typeof c === "object" && (c as { type?: string }).type === "text") as { text?: string } | undefined;
    text = first?.text ?? "";
  }
  const line = text.split("\n").find((l) => l.trim() !== "") ?? "";
  return line ? truncate(line, RESULT_SUMMARY_MAX) : undefined;
}

export interface UsageSnapshot {
  cost_usd_total: number;
  input_tokens: number;
  output_tokens: number;
  turns: number;
}

export function usageFromResult(r: SDKResultMessage): UsageSnapshot {
  let input = 0;
  let output = 0;
  for (const m of Object.values(r.modelUsage ?? {})) {
    input += m.inputTokens + m.cacheReadInputTokens + m.cacheCreationInputTokens;
    output += m.outputTokens;
  }
  return { cost_usd_total: r.total_cost_usd, input_tokens: input, output_tokens: output, turns: r.num_turns };
}

export class Mapper {
  sessionId = "";
  model = "";
  private readonly tools = new Map<string, string>();
  private started = false;

  private readonly workspace: string;

  constructor(workspace: string) {
    this.workspace = workspace;
  }

  map(msg: SDKMessage): EventBody[] {
    if (msg.session_id) this.sessionId = msg.session_id;
    switch (msg.type) {
      case "system":
        if (msg.subtype === "init" && !this.started) {
          this.started = true;
          this.model = msg.model;
          return [{ type: "started", session_id: msg.session_id, model: msg.model }];
        }
        return [];
      case "assistant": {
        // Subagent frames are folded into the parent's tool_use; skip their detail.
        if (msg.parent_tool_use_id) return [];
        const out: EventBody[] = [];
        for (const block of msg.message.content) {
          if (block.type === "text" && block.text.trim()) {
            out.push({ type: "assistant_text", text: block.text.length > TEXT_MAX ? block.text.slice(0, TEXT_MAX) + "…" : block.text });
          } else if (block.type === "tool_use") {
            this.tools.set(block.id, block.name);
            if (block.name === ASK_TOOL) continue; // surfaced as `question`
            out.push({ type: "tool_use", tool: block.name, summary: toolSummary(block.name, block.input, this.workspace) });
          }
        }
        return out;
      }
      case "user": {
        if (msg.parent_tool_use_id) return [];
        const content = msg.message.content;
        if (typeof content === "string") return [];
        const out: EventBody[] = [];
        for (const block of content) {
          if (block.type !== "tool_result") continue;
          const tool = this.tools.get(block.tool_use_id) ?? "unknown";
          if (tool === ASK_TOOL) continue;
          const ok = !block.is_error;
          const ev: EventBody = { type: "tool_result", tool, ok };
          if (!ok) {
            const summary = resultSummary(block.content);
            if (summary) ev.summary = summary;
          }
          out.push(ev);
        }
        return out;
      }
      case "result":
        return [{ type: "usage", ...usageFromResult(msg) }];
      default:
        return [];
    }
  }
}

// Outcome classification of an SDK result message.
export type ResultKind = "success" | "limit" | "error";

export function classifyResult(r: SDKResultMessage): ResultKind {
  if (r.subtype === "success") return r.is_error ? "error" : "success";
  if (r.subtype === "error_max_turns" || r.subtype === "error_max_budget_usd") return "limit";
  return "error";
}

export function resultText(r: SDKResultMessage): string {
  if (r.subtype === "success") {
    const t = r.result.trim();
    return t.length <= DONE_SUMMARY_MAX ? t : t.slice(0, DONE_SUMMARY_MAX - 1) + "…";
  }
  return truncate(r.errors?.join("; ") || r.subtype, DONE_SUMMARY_MAX);
}
