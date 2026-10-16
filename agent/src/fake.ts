import { readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { ATTACHMENTS_DIR, hasAttachments } from "./attachments.ts";
import { Cancelled, QuestionTimeout } from "./control.ts";
import type { Emitter } from "./emitter.ts";
import type { Session } from "./session.ts";

export const FAKE_FILE = "ANORA_FAKE.md";

// fakeSession is a scripted session used by tests (ANORA_RUNNER_FAKE=1):
// started, a few tool events, one question, wait for the answer, write a file.
// The caller commits and emits done.
export const fakeSession: Session = async ({ cmd, workspace, emitter, control }) => {
  const sessionId = cmd.resume_session_id ?? `fake-${cmd.task_id}-${Date.now()}`;
  emitter.emit({ type: "started", session_id: sessionId, model: cmd.model ?? "fake" });
  emitter.emit({ type: "assistant_text", text: `Working on: ${cmd.prompt.slice(0, 200)}` });
  emitter.emit({ type: "tool_use", tool: "Glob", summary: "Glob **/*" });
  emitter.emit({ type: "tool_result", tool: "Glob", ok: true, summary: "3 files" });
  emitter.emit({ type: "tool_use", tool: "Read", summary: "Read README.md" });
  emitter.emit({ type: "tool_result", tool: "Read", ok: true });
  if (hasAttachments()) probeAttachments(emitter);

  const questionId = "q1";
  emitter.emit({ type: "question", question_id: questionId, text: "Which greeting should I write?", options: ["hello", "hi"] });
  // Like a real session, an interrupt ends the run as done ("Interrupted by the user.").
  const interrupted = new Promise<null>((resolve) => (control.onInterrupt = () => resolve(null)));
  let answer: string;
  try {
    const got = await Promise.race([control.waitAnswer(questionId, cmd.limits.question_timeout_sec * 1000), interrupted]);
    if (got === null) return { kind: "done", session_id: sessionId, summary: "Interrupted by the user.", cost_usd_total: 0 };
    answer = got;
  } catch (err) {
    if (err instanceof QuestionTimeout) return { kind: "paused", session_id: sessionId, question_id: questionId };
    if (err instanceof Cancelled) return { kind: "cancelled", session_id: sessionId };
    throw err;
  }

  const extra: string[] = [];
  for (let m = control.takeMessage(); m !== undefined; m = control.takeMessage()) extra.push(m);

  emitter.emit({ type: "tool_use", tool: "Write", summary: `Write ${FAKE_FILE}` });
  writeFileSync(join(workspace, FAKE_FILE), `# ${answer}\n\n${cmd.prompt}\n${extra.map((m) => `\n- ${m}`).join("")}`);
  emitter.emit({ type: "tool_result", tool: "Write", ok: true });
  emitter.emit({ type: "usage", cost_usd_total: 0.01, input_tokens: 100, output_tokens: 50, turns: 2 });
  return { kind: "done", session_id: sessionId, summary: `Wrote ${FAKE_FILE} with "${answer}".`, cost_usd_total: 0.01 };
};

// probeAttachments reads every attached image and tries to write next to
// them, so tests can check the mount is there, readable and read-only.
function probeAttachments(emitter: Emitter): void {
  const files = readdirSync(ATTACHMENTS_DIR).filter((f) => !f.startsWith(".")).sort();
  for (const f of files) {
    const path = join(ATTACHMENTS_DIR, f);
    emitter.emit({ type: "tool_use", tool: "Read", summary: `Read ${path} (${readFileSync(path).length} bytes)` });
    emitter.emit({ type: "tool_result", tool: "Read", ok: true });
  }
  const probe = join(ATTACHMENTS_DIR, ".write-probe");
  emitter.emit({ type: "tool_use", tool: "Write", summary: `Write ${probe}` });
  try {
    writeFileSync(probe, "x");
    emitter.emit({ type: "tool_result", tool: "Write", ok: true });
  } catch (err) {
    const code = (err as NodeJS.ErrnoException).code ?? "error";
    emitter.emit({ type: "tool_result", tool: "Write", ok: false, summary: `${code}: ${ATTACHMENTS_DIR} is read-only` });
  }
}
