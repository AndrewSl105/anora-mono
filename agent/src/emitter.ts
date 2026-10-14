import { appendFileSync, mkdirSync, readFileSync, writeFileSync, existsSync } from "node:fs";
import { join } from "node:path";
import type { EventBody, RunnerEvent } from "./protocol.ts";

export interface EmitterOptions {
  workspace: string;
  write?: (line: string) => void;
  now?: () => Date;
}

// Emitter writes every event to stdout as one JSON line and appends it to
// <workspace>/.anora/events.jsonl so the backend can replay what it missed.
export class Emitter {
  private seq: number;
  private readonly logPath: string | null;
  private readonly write: (line: string) => void;
  private readonly now: () => Date;

  constructor(opts: EmitterOptions) {
    this.write = opts.write ?? ((line) => process.stdout.write(line));
    this.now = opts.now ?? (() => new Date());
    this.logPath = prepareLog(opts.workspace);
    this.seq = this.logPath ? lastSeq(this.logPath) : 0;
  }

  emit(body: EventBody): RunnerEvent {
    const ev = { ...body, ts: this.now().toISOString(), seq: ++this.seq } as RunnerEvent;
    const line = JSON.stringify(ev) + "\n";
    this.write(line);
    if (this.logPath) {
      try {
        appendFileSync(this.logPath, line);
      } catch {
        // The stdout stream is authoritative; the log is a best-effort replay aid.
      }
    }
    return ev;
  }
}

function prepareLog(workspace: string): string | null {
  try {
    const dir = join(workspace, ".anora");
    mkdirSync(dir, { recursive: true });
    excludeFromGit(workspace);
    return join(dir, "events.jsonl");
  } catch {
    return null;
  }
}

function excludeFromGit(workspace: string): void {
  const infoDir = join(workspace, ".git", "info");
  if (!existsSync(join(workspace, ".git"))) return;
  mkdirSync(infoDir, { recursive: true });
  const file = join(infoDir, "exclude");
  const current = existsSync(file) ? readFileSync(file, "utf8") : "";
  if (current.split("\n").some((l) => l.trim() === ".anora/")) return;
  writeFileSync(file, current + (current === "" || current.endsWith("\n") ? "" : "\n") + ".anora/\n");
}

// Seq continues across runs of the same task so (run, seq) stays monotonic in the log file.
function lastSeq(path: string): number {
  if (!existsSync(path)) return 0;
  const lines = readFileSync(path, "utf8").trimEnd().split("\n");
  for (let i = lines.length - 1; i >= 0; i--) {
    try {
      const seq = JSON.parse(lines[i]!).seq;
      if (typeof seq === "number") return seq;
    } catch {
      // skip a torn line
    }
  }
  return 0;
}
