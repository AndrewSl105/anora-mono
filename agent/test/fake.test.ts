import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn, execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createInterface } from "node:readline";

const MAIN = new URL("../src/main.ts", import.meta.url).pathname;

function repo(): string {
  const dir = mkdtempSync(join(tmpdir(), "anora-runner-"));
  const git = (...a: string[]) => execFileSync("git", a, { cwd: dir });
  git("init", "-q", "-b", "main");
  writeFileSync(join(dir, "README.md"), "# test\n");
  git("add", "-A");
  git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init");
  return dir;
}

function start(workspace: string) {
  const child = spawn(process.execPath, ["--experimental-strip-types", "--no-warnings", MAIN], {
    env: { ...process.env, ANORA_RUNNER_FAKE: "1", ANORA_WORKSPACE: workspace },
    stdio: ["pipe", "pipe", "inherit"],
  });
  const events: any[] = [];
  const waiters: Array<{ type: string; resolve: (e: any) => void }> = [];
  createInterface({ input: child.stdout! }).on("line", (l) => {
    const ev = JSON.parse(l);
    events.push(ev);
    for (const w of waiters.filter((w) => w.type === ev.type)) w.resolve(ev);
  });
  const exited = new Promise<number>((r) => child.on("exit", (c) => r(c ?? -1)));
  const send = (o: object) => child.stdin!.write(JSON.stringify(o) + "\n");
  const waitFor = (type: string) =>
    new Promise<any>((resolve) => {
      const e = events.find((e) => e.type === type);
      if (e) return resolve(e);
      waiters.push({ type, resolve });
    });
  return { child, events, send, waitFor, exited };
}

const run = (timeout = 30) => ({
  type: "run",
  task_id: 7,
  prompt: "Say hello",
  limits: { max_turns: 10, max_budget_usd: 1, max_duration_sec: 60, question_timeout_sec: timeout },
  commit: { message: "agent: say hello", author_name: "anora[bot]", author_email: "anora@example.com" },
});

test("fake session: question, answer, commit, done", async () => {
  const ws = repo();
  const r = start(ws);
  await r.waitFor("ready");
  r.send(run());
  const q = await r.waitFor("question");
  assert.equal(q.question_id, "q1");
  r.send({ type: "answer", question_id: "q1", text: "hello" });
  const code = await r.exited;
  assert.equal(code, 0);
  const types = r.events.map((e) => e.type);
  assert.deepEqual(types.slice(0, 2), ["ready", "started"]);
  assert.ok(types.includes("committed"));
  assert.equal(types.at(-1), "done");
  const seqs = r.events.map((e) => e.seq);
  assert.deepEqual(seqs, seqs.map((_, i) => i + 1));
  const done = r.events.at(-1);
  assert.equal(done.no_changes, false);
  assert.match(readFileSync(join(ws, "ANORA_FAKE.md"), "utf8"), /# hello/);
  const log = execFileSync("git", ["log", "-1", "--format=%an|%s"], { cwd: ws, encoding: "utf8" }).trim();
  assert.equal(log, "anora[bot]|agent: say hello");
  const status = execFileSync("git", ["status", "--porcelain"], { cwd: ws, encoding: "utf8" });
  assert.equal(status, "", ".anora/ must be excluded from git");
  const jsonl = readFileSync(join(ws, ".anora/events.jsonl"), "utf8").trim().split("\n");
  assert.equal(jsonl.length, r.events.length);
});

test("fake session: question timeout pauses with exit 3, resume continues seq", async () => {
  const ws = repo();
  const r = start(ws);
  r.send(run(1));
  assert.equal(await r.exited, 3);
  const paused = r.events.at(-1);
  assert.equal(paused.type, "paused");
  assert.equal(paused.reason, "question_timeout");
  const lastSeq = paused.seq;

  const r2 = start(ws);
  r2.send({ ...run(), resume_session_id: paused.session_id });
  r2.send({ type: "answer", question_id: "q1", text: "hi" });
  assert.equal(await r2.exited, 0);
  assert.equal(r2.events[0].seq, lastSeq + 1);
  const started = r2.events.find((e) => e.type === "started");
  assert.equal(started.session_id, paused.session_id);
});

test("cancel exits 2 and SIGTERM behaves like cancel", async () => {
  const r = start(repo());
  r.send(run());
  await r.waitFor("question");
  r.send({ type: "cancel" });
  assert.equal(await r.exited, 2);

  const r2 = start(repo());
  r2.send(run());
  await r2.waitFor("question");
  r2.child.kill("SIGTERM");
  assert.equal(await r2.exited, 2);
});

test("invalid command emits error but keeps running", async () => {
  const r = start(repo());
  r.child.stdin!.write("not json\n");
  const err = await r.waitFor("error");
  assert.equal(err.code, "bad_command");
  r.send({ type: "cancel" });
  assert.equal(await r.exited, 2);
});
