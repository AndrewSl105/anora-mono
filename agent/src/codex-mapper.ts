// Maps Codex SDK thread events to runner events. Pure apart from its own state
// (thread id, last reply, summed usage), so it is unit-testable with
// synthetic events.
import type { ThreadEvent, ThreadItem } from "@openai/codex-sdk";
import { DONE_SUMMARY_MAX, TEXT_MAX, truncate } from "./mapper.ts";
import type { EventBody } from "./protocol.ts";

const SUMMARY_MAX = 80;
const RESULT_SUMMARY_MAX = 120;

// Codex wraps shell commands as `bash -lc '<script>'`; show the script.
export function commandLine(command: string): string {
  const m = /^(?:\/\S*\/)?(?:ba|z)?sh -l?c '([\s\S]*)'$/.exec(command.trim());
  return (m ? m[1]!.replace(/'\\''/g, "'") : command).split("\n")[0] ?? "";
}

function firstLine(text: string): string | undefined {
  const line = text.split("\n").find((l) => l.trim() !== "") ?? "";
  return line ? truncate(line, RESULT_SUMMARY_MAX) : undefined;
}

export class CodexMapper {
  sessionId = "";
  // lastText is the latest agent message: the run's summary when it ends.
  lastText = "";
  failure = "";
  input = 0;
  output = 0;
  turns = 0;

  private readonly model: string;
  private started = false;

  constructor(model: string, resumeId?: string) {
    this.model = model;
    if (resumeId) this.sessionId = resumeId;
  }

  // start emits `started` once; a resumed thread may never send thread.started.
  start(): EventBody[] {
    if (this.started || !this.sessionId) return [];
    this.started = true;
    return [{ type: "started", session_id: this.sessionId, model: this.model }];
  }

  get summary(): string {
    const t = this.lastText.trim();
    return t.length <= DONE_SUMMARY_MAX ? t : t.slice(0, DONE_SUMMARY_MAX - 1) + "…";
  }

  map(ev: ThreadEvent): EventBody[] {
    switch (ev.type) {
      case "thread.started":
        this.sessionId = ev.thread_id;
        return this.start();
      case "turn.started":
        this.failure = "";
        return [];
      case "turn.completed":
        this.turns++;
        this.input += ev.usage.input_tokens;
        this.output += ev.usage.output_tokens;
        return [{ type: "usage", cost_usd_total: 0, input_tokens: this.input, output_tokens: this.output, turns: this.turns }];
      case "turn.failed":
        this.failure = ev.error.message;
        return [];
      case "error":
        this.failure = ev.message;
        return [];
      case "item.started":
        return this.itemStarted(ev.item);
      case "item.completed":
        return this.itemCompleted(ev.item);
      default:
        return [];
    }
  }

  private itemStarted(item: ThreadItem): EventBody[] {
    switch (item.type) {
      case "command_execution":
        return [{ type: "tool_use", tool: "Bash", summary: truncate(`Bash: ${commandLine(item.command)}`, SUMMARY_MAX) }];
      case "mcp_tool_call":
        return [{ type: "tool_use", tool: `${item.server}.${item.tool}`, summary: truncate(`${item.server}.${item.tool}`, SUMMARY_MAX) }];
      case "web_search":
        return [{ type: "tool_use", tool: "WebSearch", summary: truncate(`WebSearch ${item.query}`, SUMMARY_MAX) }];
      case "todo_list":
        return [{ type: "tool_use", tool: "TodoWrite", summary: `TodoWrite (${item.items.length} items)` }];
      default:
        return [];
    }
  }

  private itemCompleted(item: ThreadItem): EventBody[] {
    switch (item.type) {
      case "agent_message": {
        if (!item.text.trim()) return [];
        this.lastText = item.text;
        return [{ type: "assistant_text", text: item.text.length > TEXT_MAX ? item.text.slice(0, TEXT_MAX) + "…" : item.text }];
      }
      case "command_execution": {
        const ok = item.status === "completed" && (item.exit_code ?? 0) === 0;
        const ev: EventBody = { type: "tool_result", tool: "Bash", ok };
        const summary = ok ? undefined : firstLine(item.aggregated_output);
        return [summary ? { ...ev, summary } : ev];
      }
      case "file_change": {
        // A patch is reported once, when it is applied: emit both halves.
        const paths = item.changes.map((c) => c.path).join(", ");
        const ok = item.status === "completed";
        return [
          { type: "tool_use", tool: "Edit", summary: truncate(`Edit ${paths}`, SUMMARY_MAX) },
          ok ? { type: "tool_result", tool: "Edit", ok } : { type: "tool_result", tool: "Edit", ok, summary: "patch failed" },
        ];
      }
      case "mcp_tool_call": {
        const tool = `${item.server}.${item.tool}`;
        if (item.status === "completed") return [{ type: "tool_result", tool, ok: true }];
        const summary = item.error?.message ? truncate(item.error.message, RESULT_SUMMARY_MAX) : undefined;
        return [summary ? { type: "tool_result", tool, ok: false, summary } : { type: "tool_result", tool, ok: false }];
      }
      case "web_search":
        return [{ type: "tool_result", tool: "WebSearch", ok: true }];
      case "todo_list":
        return [{ type: "tool_result", tool: "TodoWrite", ok: true }];
      default:
        return [];
    }
  }
}
