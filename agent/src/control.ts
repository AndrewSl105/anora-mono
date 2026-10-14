import { createInterface } from "node:readline";
import type { Readable } from "node:stream";
import { parseCommand, type RunCommand } from "./protocol.ts";

export class QuestionTimeout extends Error {
  readonly questionId: string;
  constructor(questionId: string) {
    super(`no answer to ${questionId}`);
    this.questionId = questionId;
  }
}

export class Cancelled extends Error {
  constructor() {
    super("cancelled");
  }
}

type Pending = { resolve: (text: string) => void; reject: (err: Error) => void; timer: NodeJS.Timeout };

// Control turns stdin lines into the session's inputs: the first `run`, answers
// to pending questions, injected messages, interrupts and cancellation.
export class Control {
  readonly abort = new AbortController();
  private runResolve!: (cmd: RunCommand) => void;
  readonly run: Promise<RunCommand>;
  private gotRun = false;
  private readonly pending = new Map<string, Pending>();
  private readonly earlyAnswers = new Map<string, string>();
  private readonly messages: string[] = [];
  private messageWaiter: (() => void) | null = null;
  private inputClosed = false;
  onInterrupt: () => void = () => {};
  onInvalid: (error: string) => void = () => {};

  constructor(input: Readable) {
    this.run = new Promise((resolve) => (this.runResolve = resolve));
    const rl = createInterface({ input, crlfDelay: Infinity });
    rl.on("line", (line) => this.handle(line));
    rl.on("close", () => {
      this.inputClosed = true;
      this.messageWaiter?.();
    });
  }

  get cancelled(): boolean {
    return this.abort.signal.aborted;
  }

  cancel(): void {
    if (this.cancelled) return;
    this.abort.abort();
    for (const [, p] of this.pending) {
      clearTimeout(p.timer);
      p.reject(new Cancelled());
    }
    this.pending.clear();
    this.messageWaiter?.();
  }

  handle(line: string): void {
    if (line.trim() === "") return;
    const res = parseCommand(line);
    if (!res.ok) {
      this.onInvalid(res.error);
      return;
    }
    const cmd = res.command;
    switch (cmd.type) {
      case "run":
        if (this.gotRun) return this.onInvalid("duplicate run command");
        this.gotRun = true;
        this.runResolve(cmd);
        return;
      case "answer": {
        const p = this.pending.get(cmd.question_id);
        if (!p) {
          this.earlyAnswers.set(cmd.question_id, cmd.text);
          return;
        }
        clearTimeout(p.timer);
        this.pending.delete(cmd.question_id);
        p.resolve(cmd.text);
        return;
      }
      case "message":
        this.messages.push(cmd.text);
        this.messageWaiter?.();
        return;
      case "interrupt":
        this.onInterrupt();
        return;
      case "cancel":
        this.cancel();
        return;
    }
  }

  // waitAnswer resolves with the answer, or rejects with QuestionTimeout / Cancelled.
  waitAnswer(questionId: string, timeoutMs: number): Promise<string> {
    if (this.cancelled) return Promise.reject(new Cancelled());
    const early = this.earlyAnswers.get(questionId);
    if (early !== undefined) {
      this.earlyAnswers.delete(questionId);
      return Promise.resolve(early);
    }
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(questionId);
        reject(new QuestionTimeout(questionId));
      }, timeoutMs);
      this.pending.set(questionId, { resolve, reject, timer });
    });
  }

  hasPendingQuestion(): boolean {
    return this.pending.size > 0;
  }

  takeMessage(): string | undefined {
    return this.messages.shift();
  }

  // nextMessage waits for the next injected message; resolves undefined when
  // stdin closes or the run is cancelled.
  async nextMessage(): Promise<string | undefined> {
    for (;;) {
      const m = this.messages.shift();
      if (m !== undefined) return m;
      if (this.inputClosed || this.cancelled || this.messagesEnded) return undefined;
      await new Promise<void>((r) => (this.messageWaiter = r));
      this.messageWaiter = null;
    }
  }

  private messagesEnded = false;

  // endMessages makes pending and future nextMessage() calls resolve undefined.
  endMessages(): void {
    this.messagesEnded = true;
    this.messageWaiter?.();
  }
}
