import { test } from "node:test";
import assert from "node:assert/strict";
import { parseCommand } from "../src/protocol.ts";
import { parseShortstat } from "../src/git.ts";

const run = {
  type: "run",
  task_id: 42,
  prompt: "do it",
  limits: { max_turns: 10, max_budget_usd: 1, max_duration_sec: 60, question_timeout_sec: 30 },
  commit: { message: "m", author_name: "a", author_email: "a@b" },
};

test("parses a run command and stringifies task_id", () => {
  const r = parseCommand(JSON.stringify(run));
  assert.ok(r.ok);
  assert.equal(r.command.type, "run");
  if (r.command.type === "run") assert.equal(r.command.task_id, "42");
});

test("parses answer, message, interrupt, cancel", () => {
  for (const c of [
    { type: "answer", question_id: "q1", text: "yes" },
    { type: "message", text: "also this" },
    { type: "interrupt" },
    { type: "cancel" },
  ]) {
    const r = parseCommand(JSON.stringify(c));
    assert.ok(r.ok, JSON.stringify(c));
  }
});

test("rejects invalid JSON, unknown type and missing fields", () => {
  assert.equal(parseCommand("{nope").ok, false);
  assert.equal(parseCommand(JSON.stringify({ type: "explode" })).ok, false);
  const r = parseCommand(JSON.stringify({ ...run, limits: undefined }));
  assert.equal(r.ok, false);
  if (!r.ok) assert.match(r.error, /limits/);
  assert.equal(parseCommand(JSON.stringify({ type: "answer", text: "x" })).ok, false);
});

test("parses git shortstat", () => {
  assert.deepEqual(parseShortstat(" 3 files changed, 10 insertions(+), 2 deletions(-)\n"), { files_changed: 3, insertions: 10, deletions: 2 });
  assert.deepEqual(parseShortstat(" 1 file changed, 1 insertion(+)\n"), { files_changed: 1, insertions: 1, deletions: 0 });
});
