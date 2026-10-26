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

test("claude.ai artifact and share links get an explained no", () => {
  const id = "0b9e3f6a-1c2d-4e5f-8a9b-0c1d2e3f4a5b";
  for (const url of [`https://claude.ai/public/artifacts/${id}`, `https://claude.ai/public/artifacts/${id}/embed`, `https://claude.ai/share/${id}`, `https://claude.ai/api/published_artifacts/${id}`]) {
    const d = checkTool("WebFetch", { url, prompt: "content" }, WS);
    assert.equal(d.allow, false, url);
    if (!d.allow) assert.match(d.reason, /paste/);
    assert.equal(checkBash(`curl -sL ${url}`).allow, false, url);
    assert.equal(checkBash(`wget -qO- '${url}'`).allow, false, url);
  }
  assert.equal(checkTool("WebFetch", { url: "https://docs.claude.com/en/docs", prompt: "x" }, WS).allow, true);
  assert.equal(checkTool("WebFetch", { url: "https://claude.ai/", prompt: "x" }, WS).allow, true);
  assert.equal(checkBash("curl -s https://example.com/share/x").allow, true);
});

test("attachments are readable but read-only", () => {
  for (const p of ["/attachments/1.png", "../attachments/2.jpg", "/attachments"]) {
    for (const tool of ["Write", "Edit"]) {
      const d = checkTool(tool, { file_path: p }, WS);
      assert.equal(d.allow, false, `${tool} ${p}`);
      if (!d.allow) assert.match(d.reason, /read-only/);
    }
  }
  assert.equal(checkTool("Read", { file_path: "/attachments/1.png" }, WS).allow, true);
  assert.equal(checkTool("Glob", { pattern: "/attachments/*" }, WS).allow, true);
  for (const cmd of [
    "rm /attachments/1.png",
    "mv /attachments/1.png x.png",
    "echo x > /attachments/note.txt",
    "cp x.png /attachments/1.png",
    "sed -i s/a/b/ /attachments/1.png",
    "touch /attachments/new",
  ]) {
    assert.equal(checkBash(cmd).allow, false, cmd);
  }
  for (const cmd of ["ls -la /attachments", "file /attachments/1.png", "cp /attachments/1.png public/mock.png", "cat /attachments/1.png | wc -c"]) {
    assert.equal(checkBash(cmd).allow, true, cmd);
  }
});

test("other agents' key names are not Claude credentials", () => {
  assert.equal(checkBash("grep -rn OPENAI_API_KEY src").allow, true);
  assert.equal(checkBash("CURSOR_API_KEY=test npm test").allow, true);
});
