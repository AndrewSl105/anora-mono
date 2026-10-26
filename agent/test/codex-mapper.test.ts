import { test } from "node:test";
import assert from "node:assert/strict";
import type { ThreadEvent } from "@openai/codex-sdk";
import { CodexMapper, commandLine } from "../src/codex-mapper.ts";

// Synthetic events carry only the fields the mapper reads.
const e = (o: object) => o as unknown as ThreadEvent;

test("thread.started → started once; a resumed thread starts from its id", () => {
  const mp = new CodexMapper("gpt-x");
  assert.deepEqual(mp.start(), []);
  assert.deepEqual(mp.map(e({ type: "thread.started", thread_id: "t1" })), [{ type: "started", session_id: "t1", model: "gpt-x" }]);
  assert.deepEqual(mp.map(e({ type: "thread.started", thread_id: "t1" })), []);

  const resumed = new CodexMapper("default", "t0");
  assert.deepEqual(resumed.start(), [{ type: "started", session_id: "t0", model: "default" }]);
  assert.deepEqual(resumed.map(e({ type: "thread.started", thread_id: "t0" })), []);
});

test("commands become Bash tool events with the script and a failure line", () => {
  const mp = new CodexMapper("m");
  const cmd = { id: "i1", type: "command_execution", command: "/bin/bash -lc 'npm test\nmore'", aggregated_output: "", status: "in_progress" };
  assert.deepEqual(mp.map(e({ type: "item.started", item: cmd })), [{ type: "tool_use", tool: "Bash", summary: "Bash: npm test" }]);
  assert.deepEqual(mp.map(e({ type: "item.completed", item: { ...cmd, status: "completed", exit_code: 0, aggregated_output: "ok" } })), [
    { type: "tool_result", tool: "Bash", ok: true },
  ]);
  assert.deepEqual(mp.map(e({ type: "item.completed", item: { ...cmd, status: "failed", exit_code: 1, aggregated_output: "\nFAIL x.test.ts\n" } })), [
    { type: "tool_result", tool: "Bash", ok: false, summary: "FAIL x.test.ts" },
  ]);
  assert.equal(commandLine("bash -lc 'echo '\\''hi'\\'''"), "echo 'hi'");
  assert.equal(commandLine("ls -la"), "ls -la");
});

test("file changes emit a tool_use/tool_result pair; messages become text and the summary", () => {
  const mp = new CodexMapper("m");
  const fc = { id: "f", type: "file_change", changes: [{ path: "a.ts", kind: "update" }, { path: "b.ts", kind: "add" }], status: "completed" };
  assert.deepEqual(mp.map(e({ type: "item.completed", item: fc })), [
    { type: "tool_use", tool: "Edit", summary: "Edit a.ts, b.ts" },
    { type: "tool_result", tool: "Edit", ok: true },
  ]);
  assert.deepEqual(mp.map(e({ type: "item.completed", item: { id: "r", type: "reasoning", text: "hmm" } })), []);
  assert.deepEqual(mp.map(e({ type: "item.completed", item: { id: "m", type: "agent_message", text: "Done: added export." } })), [
    { type: "assistant_text", text: "Done: added export." },
  ]);
  assert.equal(mp.summary, "Done: added export.");
});

test("usage sums across turns; failures are recorded and reset by a new turn", () => {
  const mp = new CodexMapper("m");
  const usage = { input_tokens: 100, cached_input_tokens: 40, cache_write_input_tokens: 0, output_tokens: 10, reasoning_output_tokens: 3 };
  mp.map(e({ type: "turn.completed", usage }));
  assert.deepEqual(mp.map(e({ type: "turn.completed", usage })), [{ type: "usage", cost_usd_total: 0, input_tokens: 200, output_tokens: 20, turns: 2 }]);
  mp.map(e({ type: "turn.failed", error: { message: "401 Unauthorized" } }));
  assert.equal(mp.failure, "401 Unauthorized");
  mp.map(e({ type: "turn.started" }));
  assert.equal(mp.failure, "");
  mp.map(e({ type: "error", message: "stream broke" }));
  assert.equal(mp.failure, "stream broke");
});
