// Images attached to the task are mounted read-only here by Fleetpocket Runner
// (only when the task has any). FLEETPOCKET_ATTACHMENTS overrides it in tests.
import { existsSync } from "node:fs";

export const ATTACHMENTS_DIR = process.env.FLEETPOCKET_ATTACHMENTS ?? "/attachments";

export function hasAttachments(): boolean {
  return existsSync(ATTACHMENTS_DIR);
}
