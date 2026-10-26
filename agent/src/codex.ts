// Real Codex session via @openai/codex-sdk (it spawns the bundled `codex` CLI).
import { Codex, type Input } from "@openai/codex-sdk";
import { ATTACHMENTS_DIR, hasAttachments, promptImages } from "./attachments.ts";
import { CodexMapper } from "./codex-mapper.ts";
import type { Session } from "./session.ts";
import { AUTH_VARS, stateSubdir } from "./state.ts";

export const CODEX_DEFAULT_MODEL = "default";

export const codexSession: Session = async ({ cmd, workspace, emitter, control }) => {
  const apiKey = process.env.OPENAI_API_KEY;
  // The CLI gets the key as CODEX_API_KEY from the SDK; nothing else needs it.
  const env: Record<string, string> = {};
  for (const [k, v] of Object.entries(process.env)) if (v !== undefined && !AUTH_VARS.includes(k)) env[k] = v;
  env.CODEX_HOME = stateSubdir("codex");

  const codex = new Codex({
    ...(apiKey ? { apiKey } : {}),
    env,
    config: {
      ...(cmd.append_system_prompt ? { developer_instructions: cmd.append_system_prompt } : {}),
      // Commands Codex runs must not see the key it was given.
      shell_environment_policy: { inherit: "all", exclude: ["*KEY*", "*TOKEN*", "*SECRET*"] },
    },
  });
  // The container is the sandbox (no Docker, no host network, no GitHub token),
  // and nobody can approve a request.
  const threadOptions = {
    ...(cmd.model ? { model: cmd.model } : {}),
    workingDirectory: workspace,
    sandboxMode: "danger-full-access" as const,
    approvalPolicy: "never" as const,
    webSearchMode: "live" as const,
    ...(hasAttachments() ? { additionalDirectories: [ATTACHMENTS_DIR] } : {}),
  };
  const thread = cmd.resume_session_id ? codex.resumeThread(cmd.resume_session_id, threadOptions) : codex.startThread(threadOptions);

  const mapper = new CodexMapper(cmd.model ?? CODEX_DEFAULT_MODEL, cmd.resume_session_id);
  for (const ev of mapper.start()) emitter.emit(ev);

  let interrupted = false;
  let turnAbort: AbortController | null = null;
  control.onInterrupt = () => {
    interrupted = true;
    turnAbort?.abort();
  };
  const onCancel = () => turnAbort?.abort();
  control.abort.signal.addEventListener("abort", onCancel, { once: true });

  const images = promptImages(cmd.prompt);
  let input: Input = images.length ? [{ type: "text", text: cmd.prompt }, ...images.map((path) => ({ type: "local_image" as const, path }))] : cmd.prompt;
  try {
    // One turn per input: the prompt, then each message sent during the run.
    for (;;) {
      turnAbort = new AbortController();
      try {
        const { events } = await thread.runStreamed(input, { signal: turnAbort.signal });
        for await (const ev of events) for (const out of mapper.map(ev)) emitter.emit(out);
      } catch (err) {
        if (!turnAbort.signal.aborted) mapper.failure ||= err instanceof Error ? err.message : String(err);
      }
      if (control.cancelled || interrupted || mapper.failure) break;
      const next = control.takeMessage();
      if (next === undefined) break;
      input = next;
    }
  } finally {
    control.endMessages();
    control.onInterrupt = () => {};
    control.abort.signal.removeEventListener("abort", onCancel);
  }

  const session_id = mapper.sessionId || thread.id || "";
  if (control.cancelled) return { kind: "cancelled", session_id };
  if (interrupted) return { kind: "done", session_id, summary: "Interrupted by the user.", cost_usd_total: 0 };
  if (mapper.failure) return { kind: "error", code: "sdk_error", message: mapper.failure, ...(session_id ? { session_id } : {}) };
  return { kind: "done", session_id, summary: mapper.summary, cost_usd_total: 0 };
};
