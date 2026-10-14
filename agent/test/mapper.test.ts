import { test } from "node:test";
import assert from "node:assert/strict";
import type { SDKMessage, SDKResultMessage } from "@anthropic-ai/claude-agent-sdk";
import { Mapper, classifyResult, resultText, toolSummary, DONE_SUMMARY_MAX } from "../src/mapper.ts";
import { outcome } from "../src/claude.ts";

const WS = "/workspace";
const sid = "11111111-2222-3333-4444-555555555555";

// Synthetic SDK messages carry only the fields the mapper reads.
const m = (o: object) => o as unknown as SDKMessage;

function result(over: Partial<Record<string, unknown>> = {}): SDKResultMessage {
  return {
    type: "result",
    subtype: "success",
    is_error: false,
    num_turns: 3,
    result: "Added CSV export.",
    total_cost_usd: 0.42,
    session_id: sid,
    modelUsage: {
      "claude-x": { inputTokens: 100, outputTokens: 50, cacheReadInputTokens: 10, cacheCreationInputTokens: 5, webSearchRequests: 0, costUSD: 0.42, contextWindow: 1, maxOutputTokens: 1 },
    },
    ...over,
  } as unknown as SDKResultMessage;
}

test("system init → started once", () => {
  const mp = new Mapper(WS);
  const init = m({ type: "system", subtype: "init", model: "claude-x", session_id: sid });
  assert.deepEqual(mp.map(init), [{ type: "started", session_id: sid, model: "claude-x" }]);
  assert.deepEqual(mp.map(init), []);
  assert.equal(mp.sessionId, sid);
});

test("assistant text and tool_use; tool_result ok and error", () => {
  const mp = new Mapper(WS);
  const evs = mp.map(
    m({
      type: "assistant",
      parent_tool_use_id: null,
      session_id: sid,
      message: {
        content: [
          { type: "text", text: "Let me look." },
          { type: "tool_use", id: "t1", name: "Edit", input: { file_path: "/workspace/src/tables/export.ts", old_string: "a", new_string: "b" } },
          { type: "tool_use", id: "t2", name: "Bash", input: { command: "npm test -- --runInBand\necho done" } },
        ],
      },
    }),
  );
  assert.deepEqual(evs, [
    { type: "assistant_text", text: "Let me look." },
    { type: "tool_use", tool: "Edit", summary: "Edit src/tables/export.ts" },
    { type: "tool_use", tool: "Bash", summary: "Bash: npm test -- --runInBand" },
  ]);
  const res = mp.map(
    m({
      type: "user",
      parent_tool_use_id: null,
      session_id: sid,
      message: {
        role: "user",
        content: [
          { type: "tool_result", tool_use_id: "t1", content: "SECRET FILE CONTENTS" },
          { type: "tool_result", tool_use_id: "t2", is_error: true, content: [{ type: "text", text: "\nExit code 1\nFAIL src/x.test.ts" }] },
        ],
      },
    }),
  );
  assert.deepEqual(res, [
    { type: "tool_result", tool: "Edit", ok: true },
    { type: "tool_result", tool: "Bash", ok: false, summary: "Exit code 1" },
  ]);
});

test("AskUserQuestion and subagent frames are not mapped to tool events", () => {
  const mp = new Mapper(WS);
  assert.deepEqual(
    mp.map(m({ type: "assistant", parent_tool_use_id: null, session_id: sid, message: { content: [{ type: "tool_use", id: "a", name: "AskUserQuestion", input: {} }] } })),
    [],
  );
  assert.deepEqual(
    mp.map(m({ type: "user", parent_tool_use_id: null, session_id: sid, message: { role: "user", content: [{ type: "tool_result", tool_use_id: "a", content: "x" }] } })),
    [],
  );
  assert.deepEqual(
    mp.map(m({ type: "assistant", parent_tool_use_id: "p", session_id: sid, message: { content: [{ type: "text", text: "sub" }] } })),
    [],
  );
});

test("result → usage", () => {
  const mp = new Mapper(WS);
  assert.deepEqual(mp.map(result()), [{ type: "usage", cost_usd_total: 0.42, input_tokens: 115, output_tokens: 50, turns: 3 }]);
});

test("tool summaries are short and relative", () => {
  assert.equal(toolSummary("Read", { file_path: "/workspace/a/b.ts" }, WS), "Read a/b.ts");
  assert.equal(toolSummary("Read", { file_path: "/etc/hosts" }, WS), "Read /etc/hosts");
  assert.equal(toolSummary("Grep", { pattern: "foo", path: "/workspace/src" }, WS), "Grep foo in src");
  assert.equal(toolSummary("TodoWrite", { todos: [1, 2] }, WS), "TodoWrite (2 items)");
  assert.ok(toolSummary("Bash", { command: "x".repeat(500) }, WS).length <= 80);
  assert.equal(toolSummary("Mystery", {}, WS), "Mystery");
});

test("classifyResult and resultText", () => {
  assert.equal(classifyResult(result()), "success");
  assert.equal(classifyResult(result({ subtype: "error_max_turns", errors: [] })), "limit");
  assert.equal(classifyResult(result({ subtype: "error_max_budget_usd", errors: [] })), "limit");
  assert.equal(classifyResult(result({ subtype: "error_during_execution", errors: ["boom"] })), "error");
  assert.equal(classifyResult(result({ is_error: true })), "error");
  assert.equal(resultText(result({ subtype: "error_during_execution", errors: ["boom", "bang"] })), "boom; bang");
  assert.equal(resultText(result({ result: "line1\nline2" })), "line1\nline2");
  assert.equal(resultText(result({ result: "y".repeat(5000) })).length, DONE_SUMMARY_MAX);
});

test("outcome mapping", () => {
  const base = { last: null, failure: null, pausedQuestion: null, cancelled: false, interrupted: false, mapper: { sessionId: sid } };
  assert.deepEqual(outcome({ ...base, pausedQuestion: "q2" }), { kind: "paused", session_id: sid, question_id: "q2" });
  assert.deepEqual(outcome({ ...base, cancelled: true }), { kind: "cancelled", session_id: sid });
  assert.equal(outcome({ ...base, failure: new Error("spawn failed") }).kind, "error");
  assert.deepEqual(outcome({ ...base, last: result() }), { kind: "done", session_id: sid, summary: "Added CSV export.", cost_usd_total: 0.42 });
  assert.equal(outcome({ ...base, last: result({ subtype: "error_max_turns", errors: [] }) }).kind, "limit");
  assert.equal(outcome({ ...base, last: result({ subtype: "error_during_execution", errors: ["x"] }) }).kind, "error");
  const intr = outcome({ ...base, interrupted: true, last: result({ subtype: "error_during_execution", errors: ["aborted"] }) });
  assert.equal(intr.kind, "done");
});
