// Per-task agent state lives on the volume Fleetpocket mounts at ~/.claude, so
// a session can resume in a new container. Codex and Cursor keep theirs in
// subfolders of it (CODEX_HOME, and ~/.cursor, a symlink in the image).
import { mkdirSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

export const STATE_DIR = process.env.FLEETPOCKET_STATE_DIR ?? join(homedir(), ".claude");

export function stateSubdir(name: string): string {
  const dir = join(STATE_DIR, name);
  mkdirSync(dir, { recursive: true });
  return dir;
}

// Auth variables of every agent, kept out of the environment Codex passes on.
export const AUTH_VARS = ["CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "CODEX_API_KEY", "CURSOR_API_KEY"];
