import { test } from "node:test";
import assert from "node:assert/strict";
import { checkBash, checkTool } from "../src/policy.ts";

const WS = "/workspace";

const denied = [
  "git push",
  "git push origin HEAD",
  "cd x && git push",
  "git -C . push",
  "git -c user.name=x push --force",
  "/usr/bin/git push",
  "sudo git push",
  "true; git push",
  "echo hi | xargs git push",
  'bash -c "git push origin main"',
  "sh -c 'git remote add evil https://x'",
  "eval git push",
  "git remote -v",
  "git remote set-url origin https://evil",
  "git config --global user.name x",
  "git config --system core.editor vi",
  "git credential fill",
  "git credential-store get",
  "docker ps",
  "sudo docker ps",
  "/usr/bin/docker run alpine",
  "FOO=1 docker build .",
  "timeout 5 docker ps",
  "docker-compose up",
  "env",
  "env | grep TOKEN",
  "env -0",
  "printenv",
  "export -p",
  "set",
  "echo $ANTHROPIC_API_KEY",
  "echo ${CLAUDE_CODE_OAUTH_TOKEN}",
  "cat /proc/1/environ",
  "cat /proc/self/environ | tr '\\0' '\\n'",
  "echo x > .github/workflows/ci.yml",
  "echo x >> ./.github/workflows/ci.yml",
  "cat a.yml | tee .github/workflows/a.yml",
  "cp ci.yml .github/workflows/ci.yml",
  "rm -rf .github",
  "mkdir -p .github/workflows",
  "sed -i 's/a/b/' .github/workflows/ci.yml",
  "git rm .github/workflows/ci.yml",
  "git checkout main -- .github/workflows/ci.yml",
];

const allowed = [
  "npm test",
  "go test ./...",
  "git status",
  "git diff HEAD~1",
  "git log --oneline -5",
  "git config user.name",
  "git add -A",
  "git commit -m 'fix'",
  "grep -rn docker docs/",
  'grep -rn "git push" README.md',
  "cat .github/workflows/ci.yml",
  "ls .github/workflows",
  "env NODE_ENV=test npm test",
  "printenv HOME",
  "echo $NODE_ENV",
  "export FOO=bar",
  "cat Dockerfile",
];

test("denies dangerous bash commands", () => {
  for (const c of denied) {
    const d = checkBash(c);
    assert.equal(d.allow, false, `should deny: ${c}`);
    if (!d.allow) assert.ok(d.reason.length > 10);
  }
});

test("allows ordinary bash commands", () => {
  for (const c of allowed) assert.equal(checkBash(c).allow, true, `should allow: ${c}`);
});

test("denies writes under .github/ for file tools", () => {
  assert.equal(checkTool("Write", { file_path: "/workspace/.github/workflows/ci.yml" }, WS).allow, false);
  assert.equal(checkTool("Edit", { file_path: ".github/dependabot.yml" }, WS).allow, false);
  assert.equal(checkTool("MultiEdit", { file_path: "/workspace/sub/.github/x.yml" }, WS).allow, false);
  assert.equal(checkTool("Write", { file_path: "/workspace/src/github.ts" }, WS).allow, true);
  assert.equal(checkTool("Write", { file_path: "/workspace/.githubx/a" }, WS).allow, true);
  assert.equal(checkTool("Read", { file_path: "/workspace/.github/workflows/ci.yml" }, WS).allow, true);
});

test("denies reads of /proc/*/environ", () => {
  assert.equal(checkTool("Read", { file_path: "/proc/1/environ" }, WS).allow, false);
  assert.equal(checkTool("Read", { file_path: "/proc/self/environ" }, WS).allow, false);
  assert.equal(checkTool("Grep", { pattern: "TOKEN", path: "/proc/self/environ" }, WS).allow, false);
  assert.equal(checkTool("Read", { file_path: "/proc/cpuinfo" }, WS).allow, true);
});

test("Bash tool goes through checkBash", () => {
  assert.equal(checkTool("Bash", { command: "git push" }, WS).allow, false);
  assert.equal(checkTool("Bash", { command: "npm test" }, WS).allow, true);
});
