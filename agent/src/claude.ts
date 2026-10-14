import type { Session } from "./session.ts";

// Real Claude Code session via @anthropic-ai/claude-agent-sdk. Implemented in phase 5.
export const claudeSession: Session = async () => ({
  kind: "error",
  code: "not_implemented",
  message: "real Claude sessions are not implemented yet; set ANORA_RUNNER_FAKE=1",
});
