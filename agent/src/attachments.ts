// Images attached to the task are mounted read-only here by Anora Runner
// (only when the task has any). ANORA_ATTACHMENTS overrides it in tests.
import { existsSync } from "node:fs";

export const ATTACHMENTS_DIR = process.env.ANORA_ATTACHMENTS ?? "/attachments";

export function hasAttachments(): boolean {
  return existsSync(ATTACHMENTS_DIR);
}
