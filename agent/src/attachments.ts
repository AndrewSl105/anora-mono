// Images attached to the task are mounted read-only here by Fleetpocket
// (only when the task has any). FLEETPOCKET_ATTACHMENTS overrides it in tests.
import { existsSync } from "node:fs";

export const ATTACHMENTS_DIR = process.env.FLEETPOCKET_ATTACHMENTS ?? "/attachments";

export function hasAttachments(): boolean {
  return existsSync(ATTACHMENTS_DIR);
}

// promptImages returns the paths in the prompt's "Attached images" block (the
// images sent with this run's input; see docs/PROTOCOL.md, "Attachments").
// Codex and Cursor get them as image inputs instead of reading them with a tool.
export function promptImages(prompt: string): string[] {
  const block = prompt.split(/\n\n/).find((b) => b.startsWith("Attached images"));
  if (!block) return [];
  return [...block.matchAll(/^- (\S+) \(/gm)].map((m) => m[1]!).filter((p) => p.startsWith(ATTACHMENTS_DIR + "/"));
}
