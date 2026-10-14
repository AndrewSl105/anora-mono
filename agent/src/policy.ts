// Tool policy enforced by the PreToolUse hook. Pure functions so they can be
// unit-tested without the SDK. The backend's diff validation (no .github/
// changes, secrets scan) is the second line of defence; this hook gives
// Claude an early, explained "no" instead of a failed push.
import { isAbsolute, normalize, relative } from "node:path";

export type PolicyDecision = { allow: true } | { allow: false; reason: string };

const ALLOW: PolicyDecision = { allow: true };
const deny = (reason: string): PolicyDecision => ({ allow: false, reason });

const AUTH_VARS = ["CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"];
const PROC_ENVIRON = /\/proc\/[^\s/'"]+\/environ\b/;

const WRITE_TOOLS = new Set(["Write", "Edit", "MultiEdit", "NotebookEdit"]);
const READ_TOOLS = new Set(["Read", "Grep", "Glob", "NotebookRead"]);

export function checkTool(toolName: string, input: unknown, workspace: string): PolicyDecision {
  const inp = (input ?? {}) as Record<string, unknown>;
  if (toolName === "Bash") return checkBash(String(inp.command ?? ""));

  const paths = ["file_path", "notebook_path", "path", "pattern"]
    .map((k) => inp[k])
    .filter((v): v is string => typeof v === "string");

  if (WRITE_TOOLS.has(toolName)) {
    for (const p of paths) {
      if (isUnderGithubDir(p, workspace)) return deny(githubReason(p));
    }
  }
  if (WRITE_TOOLS.has(toolName) || READ_TOOLS.has(toolName)) {
    for (const p of paths) {
      if (PROC_ENVIRON.test(p)) return deny(environReason);
    }
  }
  return ALLOW;
}

const environReason = "Reading process environments (/proc/*/environ) is not allowed in this sandbox.";
const githubReason = (p: string) =>
  `Changes under .github/ are not allowed (${p}). CI configuration is managed by the repository owner; describe the change you would make in your summary instead.`;

// isUnderGithubDir reports whether a path points into a `.github` directory,
// relative to the workspace or anywhere in an absolute path.
export function isUnderGithubDir(p: string, workspace: string): boolean {
  const unquoted = p.replace(/^['"]|['"]$/g, "");
  const rel = isAbsolute(unquoted) ? relative(workspace, unquoted) : normalize(unquoted);
  const segments = rel.split(/[\\/]+/);
  return segments.includes(".github");
}

// ---- Bash ----

const SEPARATORS = /\|\||&&|[;|&\n]|\$\(|`|\(|\)/;

function stripQuotes(t: string): string {
  return t.replace(/^['"]+|['"]+$/g, "");
}

// tokens splits one segment on whitespace and strips surrounding quotes. It is
// deliberately naive: quoting tricks fall into the conservative direction
// (e.g. `bash -c "git push"` still yields the tokens git, push).
function tokens(segment: string): string[] {
  return segment.split(/\s+/).map(stripQuotes).filter(Boolean);
}

const isGit = (t: string) => /(^|\/)git$/.test(t);
const isDocker = (t: string) => /(^|\/)(docker|docker-compose|dockerd|podman)$/.test(t);

// Global git options that take a separate value argument.
const GIT_OPTS_WITH_VALUE = new Set(["-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--config-env"]);

function gitSubcommand(toks: string[], start: number): { sub: string; rest: string[] } | null {
  let i = start + 1;
  while (i < toks.length) {
    const t = toks[i]!;
    if (GIT_OPTS_WITH_VALUE.has(t)) {
      i += 2;
      continue;
    }
    if (t.startsWith("-")) {
      i += 1;
      continue;
    }
    return { sub: t, rest: toks.slice(i + 1) };
  }
  return null;
}

const WRITE_COMMANDS = new Set(["tee", "cp", "mv", "rm", "rmdir", "mkdir", "touch", "ln", "install", "rsync", "truncate", "chmod", "chown", "dd", "patch", "unzip", "tar"]);
const GIT_WRITE_SUBCOMMANDS = new Set(["rm", "mv", "checkout", "restore", "apply", "am", "stash", "reset", "clean", "add"]);

export function checkBash(command: string): PolicyDecision {
  if (PROC_ENVIRON.test(command)) return deny(environReason);
  for (const v of AUTH_VARS) {
    if (command.includes(v)) return deny(`Accessing ${v} is not allowed; credentials are not available to tools.`);
  }

  // Redirections into .github/ anywhere in the command.
  const redirect = /(?:^|[^<>&\d])\d*>>?\|?\s*(['"]?)([^\s'";|&]+)\1/g;
  for (const m of command.matchAll(redirect)) {
    if (isUnderGithubDir(m[2]!, "/workspace")) return deny(githubReason(m[2]!));
  }

  for (const segment of command.split(SEPARATORS)) {
    const d = checkSegment(tokens(segment));
    if (!d.allow) return d;
  }
  return ALLOW;
}

const SHELLS = new Set(["sh", "bash", "zsh", "dash", "ksh"]);

function checkSegment(toks: string[]): PolicyDecision {
  const cmdIdx = commandIndex(toks);
  if (cmdIdx === -1) return ALLOW;
  const cmd = baseName(toks[cmdIdx]!);
  const args = toks.slice(cmdIdx + 1);

  // Nested commands: `bash -c "…"`, `eval …`.
  if (SHELLS.has(cmd)) {
    const c = args.findIndex((a) => /^-[a-zA-Z]*c$/.test(a));
    if (c !== -1) return checkBash(args.slice(c + 1).join(" "));
    return ALLOW;
  }
  if (cmd === "eval") return checkBash(args.join(" "));

  if (isDocker(cmd)) {
    return deny("Docker is not available to the agent. Run the project's tests and tools directly instead.");
  }
  if (isGit(cmd)) {
    const g = gitSubcommand(toks, cmdIdx);
    if (!g) return ALLOW;
    if (g.sub === "push") return deny("git push is not allowed. Commit locally; Anora Runner pushes the branch and opens the PR after the session.");
    if (g.sub === "remote") return deny("git remote is not allowed. The repository remote is managed by Anora Runner.");
    if (g.sub === "credential" || g.sub.startsWith("credential-")) return deny("git credential commands are not allowed.");
    if (g.sub === "config" && g.rest.some((r) => r === "--global" || r === "--system")) {
      return deny("git config --global/--system is not allowed. Use repository-local settings if needed.");
    }
    const gh = g.rest.find((r) => isUnderGithubDir(r, "/workspace"));
    if (GIT_WRITE_SUBCOMMANDS.has(g.sub) && gh) return deny(githubReason(gh));
    return ALLOW;
  }

  if (isEnvDump(cmd, args)) {
    return deny("Dumping the environment is not allowed; it contains credentials. Read specific, non-secret variables instead (e.g. echo $NODE_ENV).");
  }

  const gh = args.find((a) => isUnderGithubDir(a, "/workspace"));
  if (gh) {
    const inPlaceEdit = (cmd === "sed" || cmd === "perl") && args.some((a) => /^-[a-zA-Z]*i/.test(a));
    if (WRITE_COMMANDS.has(cmd) || inPlaceEdit) return deny(githubReason(gh));
  }
  return ALLOW;
}

const WRAPPERS = new Set(["sudo", "doas", "command", "exec", "nohup", "time", "nice", "ionice", "stdbuf", "timeout", "xargs", "builtin"]);

// commandIndex skips leading VAR=value assignments and wrapper commands.
function commandIndex(toks: string[]): number {
  let i = 0;
  while (i < toks.length) {
    const t = toks[i]!;
    if (/^[A-Za-z_][A-Za-z0-9_]*=/.test(t)) {
      i++;
      continue;
    }
    if (WRAPPERS.has(baseName(t))) {
      i++;
      while (i < toks.length && toks[i]!.startsWith("-")) i++;
      // timeout takes a duration argument
      if (baseName(t) === "timeout" && i < toks.length && /^\d/.test(toks[i]!)) i++;
      continue;
    }
    return i;
  }
  return -1;
}

function baseName(t: string): string {
  return t.slice(t.lastIndexOf("/") + 1);
}

function isEnvDump(cmd: string, args: string[]): boolean {
  switch (cmd) {
    case "env": {
      // `env FOO=1 npm test` runs a command; `env`, `env -0`, `env | grep` dump.
      const rest = args.filter((a) => !a.startsWith("-") && !/^[A-Za-z_][A-Za-z0-9_]*=/.test(a));
      return rest.length === 0;
    }
    case "printenv":
      return args.filter((a) => !a.startsWith("-")).length === 0;
    case "set":
      return args.length === 0;
    case "export":
    case "declare":
    case "typeset":
      return args.length === 0 || args.every((a) => /^-[a-zA-Z]*[px]/.test(a));
    case "compgen":
      return args.includes("-e") || args.includes("-v");
    default:
      return false;
  }
}
