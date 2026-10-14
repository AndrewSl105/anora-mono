import { execFileSync } from "node:child_process";

export interface CommitResult {
  sha: string;
  files_changed: number;
  insertions: number;
  deletions: number;
}

function git(cwd: string, args: string[]): string {
  return execFileSync("git", args, { cwd, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
}

export function hasChanges(cwd: string): boolean {
  return git(cwd, ["status", "--porcelain"]).trim() !== "";
}

// commitAll stages everything and commits with the given author. Returns null
// when the tree is clean. Never pushes.
export function commitAll(cwd: string, message: string, name: string, email: string): CommitResult | null {
  if (!hasChanges(cwd)) return null;
  git(cwd, ["add", "-A"]);
  git(cwd, ["-c", `user.name=${name}`, "-c", `user.email=${email}`, "commit", "--no-verify", "-q", "-m", message]);
  const sha = git(cwd, ["rev-parse", "HEAD"]).trim();
  return { sha, ...parseShortstat(git(cwd, ["show", "--shortstat", "--format=", "HEAD"])) };
}

export function parseShortstat(s: string): Omit<CommitResult, "sha"> {
  const num = (re: RegExp) => Number(re.exec(s)?.[1] ?? 0);
  return {
    files_changed: num(/(\d+) files? changed/),
    insertions: num(/(\d+) insertions?\(\+\)/),
    deletions: num(/(\d+) deletions?\(-\)/),
  };
}
