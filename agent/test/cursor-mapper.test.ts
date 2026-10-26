import { test } from "node:test";
import assert from "node:assert/strict";
import type { SDKMessage } from "@cursor/sdk";
import { CursorMapper, cursorToolSummary, toolFailure } from "../src/cursor-mapper.ts";

const WS = "/workspace";
const m = (o: object) => o as unknown as SDKMessage;
const base = { agent_id: "agent-1", run_id: "run-1" };

test("start emits started once with the agent id", () => {
  const mp = new CursorMapper(WS, "auto");
  assert.deepEqual(mp.start("agent-1"), [{ type: "started", session_id: "agent-1", model: "auto" }]);
  assert.deepEqual(mp.start("agent-1"), []);
  assert.equal(mp.sessionId, "agent-1");
});

test("tool calls: one tool_use per call, a result when it ends", () => {
  const mp = new CursorMapper(WS, "auto");
  const call = { ...base, type: "tool_call", call_id: "c1", name: "read", args: { path: "/workspace/src/a.ts" } };
  assert.deepEqual(mp.map(m({ ...call, status: "running" })), [{ type: "tool_use", tool: "Read", summary: "Read src/a.ts" }]);
  assert.deepEqual(mp.map(m({ ...call, status: "running" })), []);
  assert.deepEqual(mp.map(m({ ...call, status: "completed", result: { status: "success", value: { content: "x" } } })), [
    { type: "tool_result", tool: "Read", ok: true },
  ]);
  // A call seen only once it completed still gets both halves.
  const shell = { ...base, type: "tool_call", call_id: "c2", name: "shell", args: { command: "npm test" } };
  assert.deepEqual(mp.map(m({ ...shell, status: "completed", result: { status: "success", value: { exitCode: 1, stderr: "FAIL a\n", stdout: "" } } })), [
    { type: "tool_use", tool: "Bash", summary: "Bash: npm test" },
    { type: "tool_result", tool: "Bash", ok: false, summary: "FAIL a" },
  ]);
  const err = { ...base, type: "tool_call", call_id: "c3", name: "edit", args: { path: "b.ts" }, status: "error", result: { status: "error", error: { message: "no match" } } };
  assert.deepEqual(mp.map(m(err)), [
    { type: "tool_use", tool: "Edit", summary: "Edit b.ts" },
    { type: "tool_result", tool: "Edit", ok: false, summary: "no match" },
  ]);
});

test("summaries and failures", () => {
  assert.equal(cursorToolSummary("grep", { pattern: "foo", path: "/workspace/src" }, WS), "Grep foo in src");
  assert.equal(cursorToolSummary("glob", { globPattern: "**/*.ts" }, WS), "Glob **/*.ts");
  assert.equal(cursorToolSummary("mcp", { providerIdentifier: "gh", toolName: "search" }, WS), "gh.search");
  assert.equal(cursorToolSummary("semSearch", {}, WS), "semSearch");
  assert.equal(toolFailure("shell", { status: "success", value: { exitCode: 0 } }), null);
  assert.equal(toolFailure("shell", { status: "success", value: { exitCode: 2, stderr: "", stdout: "" } }), "exit 2");
  assert.equal(toolFailure("read", { status: "error", error: {} }), "");
});

test("text, usage and status", () => {
  const mp = new CursorMapper(WS, "auto");
  const text = { ...base, type: "assistant", message: { role: "assistant", content: [{ type: "text", text: "All done." }, { type: "tool_use", id: "x", name: "read", input: {} }] } };
  assert.deepEqual(mp.map(m(text)), [{ type: "assistant_text", text: "All done." }]);
  assert.equal(mp.summary, "All done.");
  const usage = { inputTokens: 10, outputTokens: 5, cacheReadTokens: 3, cacheWriteTokens: 2, totalTokens: 20 };
  mp.map(m({ ...base, type: "usage", usage }));
  assert.deepEqual(mp.map(m({ ...base, type: "usage", usage })), [{ type: "usage", cost_usd_total: 0, input_tokens: 30, output_tokens: 10, turns: 2 }]);
  mp.map(m({ ...base, type: "status", status: "ERROR", message: "unauthorized" }));
  assert.equal(mp.failure, "unauthorized");
});
