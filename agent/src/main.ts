import { Control } from "./control.ts";
import { Emitter } from "./emitter.ts";
import { fakeSession } from "./fake.ts";
import { commitAll } from "./git.ts";
import { ExitCode, RUNNER_VERSION, type RunCommand } from "./protocol.ts";
import type { Session, SessionOutcome } from "./session.ts";

const HEARTBEAT_MS = 15_000;

async function main(): Promise<number> {
  const workspace = process.env.ANORA_WORKSPACE ?? "/workspace";
  const fake = process.env.ANORA_RUNNER_FAKE === "1";
  const emitter = new Emitter({ workspace });
  const control = new Control(process.stdin);

  control.onInvalid = (message) => emitter.emit({ type: "error", code: "bad_command", message });
  process.on("SIGTERM", () => control.cancel());
  process.on("SIGINT", () => control.cancel());

  emitter.emit({ type: "ready", runner_version: RUNNER_VERSION });
  const heartbeat = setInterval(() => emitter.emit({ type: "heartbeat" }), HEARTBEAT_MS);
  try {
    const cmd = await Promise.race([control.run, cancelledPromise(control)]);
    if (!cmd) return ExitCode.Cancelled;

    if (!fake && !process.env.CLAUDE_CODE_OAUTH_TOKEN && !process.env.ANTHROPIC_API_KEY) {
      emitter.emit({ type: "error", code: "no_auth", message: "neither CLAUDE_CODE_OAUTH_TOKEN nor ANTHROPIC_API_KEY is set" });
      return ExitCode.Error;
    }

    let durationHit = false;
    const durationTimer = setTimeout(() => {
      durationHit = true;
      control.cancel();
    }, cmd.limits.max_duration_sec * 1000);

    const session: Session = fake ? fakeSession : (await import("./claude.ts")).claudeSession;
    let outcome: SessionOutcome;
    try {
      outcome = await session({ cmd, workspace, emitter, control });
    } catch (err) {
      outcome = { kind: "error", code: "internal", message: err instanceof Error ? err.message : String(err) };
    } finally {
      clearTimeout(durationTimer);
    }
    if (durationHit && outcome.kind !== "done") {
      outcome = { kind: "limit", session_id: sessionIdOf(outcome), message: `max_duration_sec ${cmd.limits.max_duration_sec} exceeded` };
    }
    return finish(cmd, outcome, workspace, emitter);
  } finally {
    clearInterval(heartbeat);
  }
}

function finish(cmd: RunCommand, outcome: SessionOutcome, workspace: string, emitter: Emitter): number {
  switch (outcome.kind) {
    case "done":
    case "limit": {
      let committed;
      try {
        committed = commitAll(workspace, cmd.commit.message, cmd.commit.author_name, cmd.commit.author_email);
      } catch (err) {
        emitter.emit({ type: "error", code: "commit_failed", message: errMessage(err), session_id: outcome.session_id });
        return ExitCode.Error;
      }
      if (committed) emitter.emit({ type: "committed", ...committed });
      if (outcome.kind === "limit") {
        emitter.emit({ type: "error", code: "limit_exceeded", message: outcome.message, session_id: outcome.session_id });
        return ExitCode.LimitExceeded;
      }
      emitter.emit({
        type: "done",
        session_id: outcome.session_id,
        summary: outcome.summary,
        no_changes: committed === null,
        cost_usd_total: outcome.cost_usd_total,
      });
      return ExitCode.Done;
    }
    case "paused":
      emitter.emit({ type: "paused", session_id: outcome.session_id, reason: "question_timeout", question_id: outcome.question_id });
      return ExitCode.Paused;
    case "cancelled":
      return ExitCode.Cancelled;
    case "error":
      emitter.emit({ type: "error", code: outcome.code, message: outcome.message, session_id: outcome.session_id });
      return ExitCode.Error;
  }
}

function sessionIdOf(o: SessionOutcome): string {
  return ("session_id" in o && o.session_id) || "";
}

function errMessage(err: unknown): string {
  if (err && typeof err === "object" && "stderr" in err && err.stderr) return String(err.stderr).trim();
  return err instanceof Error ? err.message : String(err);
}

function cancelledPromise(control: Control): Promise<undefined> {
  return new Promise((resolve) => control.abort.signal.addEventListener("abort", () => resolve(undefined)));
}

main().then(
  (code) => process.stdout.write("", () => process.exit(code)),
  (err) => {
    process.stderr.write(`fatal: ${err instanceof Error ? err.stack : err}\n`);
    process.exit(ExitCode.Error);
  },
);
