// Real Claude Code session via @anthropic-ai/claude-agent-sdk.
import { query, type CanUseTool, type HookCallback, type SDKResultMessage, type SDKUserMessage } from "@anthropic-ai/claude-agent-sdk";
import { ATTACHMENTS_DIR, hasAttachments } from "./attachments.ts";
import { QuestionTimeout } from "./control.ts";
import { ASK_TOOL, Mapper, classifyResult, resultText } from "./mapper.ts";
import { checkTool } from "./policy.ts";
import { RUNNER_VERSION } from "./protocol.ts";
import type { Session, SessionOutcome } from "./session.ts";

// Pre-approved tools. AskUserQuestion is available but deliberately NOT
// pre-approved, so every use reaches canUseTool, where it becomes a `question`.
export const ALLOWED_TOOLS = ["Read", "Write", "Edit", "Glob", "Grep", "Bash", "WebSearch", "WebFetch", "TodoWrite"];

function userMessage(text: string): SDKUserMessage {
  return { type: "user", message: { role: "user", content: text }, parent_tool_use_id: null };
}

type AskInput = { questions?: Array<{ question?: string; options?: Array<{ label?: string }> }> };

export const claudeSession: Session = async ({ cmd, workspace, emitter, control }) => {
  const mapper = new Mapper(workspace);
  // queryAbort ends the SDK session: on cancel (control.abort) and on pause.
  const queryAbort = new AbortController();
  control.abort.signal.addEventListener("abort", () => queryAbort.abort(), { once: true });

  let pausedQuestion: string | null = null;
  let questionSeq = 0;
  let interrupted = false;

  async function* input(): AsyncGenerator<SDKUserMessage> {
    yield userMessage(cmd.prompt);
    for (;;) {
      const text = await control.nextMessage();
      if (text === undefined) return;
      yield userMessage(text);
    }
  }

  const canUseTool: CanUseTool = async (toolName, toolInput, { signal }) => {
    if (toolName !== ASK_TOOL) {
      // Reached only for tools outside ALLOWED_TOOLS or edits the mode does not
      // auto-accept. Nobody can approve interactively, so the policy decides.
      const d = checkTool(toolName, toolInput, workspace);
      return d.allow ? { behavior: "allow", updatedInput: toolInput } : { behavior: "deny", message: d.reason };
    }
    const questions = (toolInput as AskInput).questions ?? [];
    const answers: Record<string, string> = {};
    for (const q of questions) {
      const text = q.question ?? "";
      if (!text) continue;
      const id = `q${++questionSeq}`;
      const options = (q.options ?? []).map((o) => o.label ?? "").filter(Boolean);
      emitter.emit({ type: "question", question_id: id, text, ...(options.length ? { options } : {}) });
      // An interrupt aborts the pending permission request; stop waiting for the answer.
      const aborted = new Promise<null>((resolve) => {
        if (signal.aborted) resolve(null);
        signal.addEventListener("abort", () => resolve(null), { once: true });
      });
      try {
        const got = await Promise.race([control.waitAnswer(id, cmd.limits.question_timeout_sec * 1000), aborted]);
        if (got === null) return { behavior: "deny", message: "Interrupted by the user.", interrupt: true };
        answers[text] = got;
      } catch (err) {
        if (err instanceof QuestionTimeout) {
          pausedQuestion = id;
          queryAbort.abort();
        }
        return { behavior: "deny", message: "The question was not answered; the session is stopping.", interrupt: true };
      }
    }
    return { behavior: "allow", updatedInput: { ...toolInput, answers } };
  };

  const preToolUse: HookCallback = async (hookInput) => {
    if (hookInput.hook_event_name !== "PreToolUse") return {};
    const d = checkTool(hookInput.tool_name, hookInput.tool_input, workspace);
    if (d.allow) return {};
    return { hookSpecificOutput: { hookEventName: "PreToolUse", permissionDecision: "deny", permissionDecisionReason: d.reason } };
  };

  const q = query({
    prompt: input(),
    options: {
      cwd: workspace,
      // Attached images live outside the workspace; the prompt lists them.
      ...(hasAttachments() ? { additionalDirectories: [ATTACHMENTS_DIR] } : {}),
      ...(cmd.model ? { model: cmd.model } : {}),
      ...(cmd.resume_session_id ? { resume: cmd.resume_session_id } : {}),
      maxTurns: cmd.limits.max_turns,
      maxBudgetUsd: cmd.limits.max_budget_usd,
      systemPrompt: { type: "preset", preset: "claude_code", ...(cmd.append_system_prompt ? { append: cmd.append_system_prompt } : {}) },
      settingSources: ["project"],
      tools: [...ALLOWED_TOOLS, ASK_TOOL],
      allowedTools: ALLOWED_TOOLS,
      permissionMode: "acceptEdits",
      canUseTool,
      hooks: { PreToolUse: [{ hooks: [preToolUse] }] },
      abortController: queryAbort,
      env: { ...process.env, CLAUDE_AGENT_SDK_CLIENT_APP: `anora-agent/${RUNNER_VERSION}` },
    },
  });

  control.onInterrupt = () => {
    interrupted = true;
    q.interrupt().catch(() => {});
  };

  let last: SDKResultMessage | null = null;
  let failure: unknown = null;
  try {
    for await (const msg of q) {
      for (const ev of mapper.map(msg)) emitter.emit(ev);
      if (msg.type === "result") {
        last = msg;
        // One prompt per run: once a turn finishes, close the input so the
        // session ends. Messages injected mid-turn were already delivered.
        control.endMessages();
      }
    }
  } catch (err) {
    failure = err;
  } finally {
    control.onInterrupt = () => {};
  }

  return outcome({ last, failure, pausedQuestion, cancelled: control.cancelled, interrupted, mapper });
};

export function outcome(s: {
  last: SDKResultMessage | null;
  failure: unknown;
  pausedQuestion: string | null;
  cancelled: boolean;
  interrupted: boolean;
  mapper: { sessionId: string };
}): SessionOutcome {
  const session_id = s.last?.session_id || s.mapper.sessionId;
  if (s.pausedQuestion) return { kind: "paused", session_id, question_id: s.pausedQuestion };
  if (s.cancelled) return { kind: "cancelled", session_id };
  if (!s.last) {
    const message = s.failure instanceof Error ? s.failure.message : s.failure ? String(s.failure) : "session ended without a result";
    return { kind: "error", code: "sdk_error", message, ...(session_id ? { session_id } : {}) };
  }
  const kind = classifyResult(s.last);
  if (kind === "limit") {
    return { kind: "limit", session_id, message: `${s.last.subtype}: ${resultText(s.last)}` };
  }
  if (kind === "error" && !s.interrupted) {
    return { kind: "error", code: "sdk_error", message: resultText(s.last), session_id };
  }
  const summary = kind === "success" ? resultText(s.last) : "Interrupted by the user.";
  return { kind: "done", session_id, summary, cost_usd_total: s.last.total_cost_usd };
}
