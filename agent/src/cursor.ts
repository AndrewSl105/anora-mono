// Real Cursor session via @cursor/sdk, as a local agent working in /workspace.
import { readFileSync } from "node:fs";
import { extname } from "node:path";
import { Agent, type AgentOptions, type Run, type SDKImage, type SDKUserMessage } from "@cursor/sdk";
import { ATTACHMENTS_DIR, hasAttachments, promptImages } from "./attachments.ts";
import { CursorMapper } from "./cursor-mapper.ts";
import type { Session } from "./session.ts";
import { stateSubdir } from "./state.ts";

// "auto" lets Cursor pick; a local agent needs a model before its first send.
export const CURSOR_DEFAULT_MODEL = "auto";

const MIME: Record<string, string> = { ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp", ".gif": "image/gif" };

function image(path: string): SDKImage {
  return { data: readFileSync(path).toString("base64"), mimeType: MIME[extname(path).toLowerCase()] ?? "image/png" };
}

export const cursorSession: Session = async ({ cmd, workspace, emitter, control }) => {
  // The SDK builds the shell's environment from process.env; take the key out.
  const apiKey = process.env.CURSOR_API_KEY;
  delete process.env.CURSOR_API_KEY;
  // ~/.cursor links here (see the Dockerfile), so the agent store survives the container.
  stateSubdir("cursor");

  const model = cmd.model || CURSOR_DEFAULT_MODEL;
  // No sandbox and no approval: the container is the sandbox and nobody can approve.
  // askQuestion would block on an answer nobody sends; tools/disallowedTools are
  // not persisted, so they are passed on resume too.
  const options: AgentOptions = {
    ...(apiKey ? { apiKey } : {}),
    model: { id: model },
    disallowedTools: ["askQuestion"],
    local: {
      cwd: workspace,
      ...(hasAttachments() ? { dirs: [ATTACHMENTS_DIR] } : {}),
      settingSources: ["project"],
      sandboxOptions: { enabled: false },
    },
  };
  let agent;
  try {
    agent = cmd.resume_session_id ? await Agent.resume(cmd.resume_session_id, options) : await Agent.create(options);
  } catch (err) {
    return { kind: "error", code: "sdk_error", message: err instanceof Error ? err.message : String(err), ...(cmd.resume_session_id ? { session_id: cmd.resume_session_id } : {}) };
  }

  const mapper = new CursorMapper(workspace, model);
  for (const ev of mapper.start(agent.agentId)) emitter.emit(ev);

  let interrupted = false;
  let run: Run | null = null;
  control.onInterrupt = () => {
    interrupted = true;
    run?.cancel().catch(() => {});
  };
  const onCancel = () => run?.cancel().catch(() => {});
  control.abort.signal.addEventListener("abort", onCancel, { once: true });

  // Cursor has no appendable system prompt (`systemPrompt` replaces its own),
  // so a new agent gets Fleetpocket's instructions ahead of the first prompt.
  const text = cmd.append_system_prompt && !cmd.resume_session_id ? `${cmd.append_system_prompt}\n\n${cmd.prompt}` : cmd.prompt;
  const images = promptImages(cmd.prompt).map(image);
  let input: string | SDKUserMessage = images.length ? { text, images } : text;
  try {
    // One run per input: the prompt, then each message sent during the run.
    for (;;) {
      if (control.cancelled) break;
      run = await agent.send(input);
      if (control.cancelled || interrupted) await run.cancel().catch(() => {});
      for await (const msg of run.stream()) for (const ev of mapper.map(msg)) emitter.emit(ev);
      const result = await run.wait();
      if (result.status === "error") mapper.failure ||= result.error?.message || "run failed";
      if (result.status !== "finished") break;
      if (result.result?.trim()) mapper.lastText = result.result;
      const next = control.takeMessage();
      if (next === undefined) break;
      input = next;
    }
  } catch (err) {
    if (!control.cancelled && !interrupted) mapper.failure ||= err instanceof Error ? err.message : String(err);
  } finally {
    control.endMessages();
    control.onInterrupt = () => {};
    control.abort.signal.removeEventListener("abort", onCancel);
    agent.close();
  }

  const session_id = mapper.sessionId;
  if (control.cancelled) return { kind: "cancelled", session_id };
  if (interrupted) return { kind: "done", session_id, summary: "Interrupted by the user.", cost_usd_total: 0 };
  if (mapper.failure) return { kind: "error", code: "sdk_error", message: mapper.failure, session_id };
  return { kind: "done", session_id, summary: mapper.summary, cost_usd_total: 0 };
};
