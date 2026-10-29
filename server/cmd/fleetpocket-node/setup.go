package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"text/template"
	"time"

	"github.com/moby/moby/client"
	"golang.org/x/term"

	"github.com/andrewsl105/fleetpocket/server/internal/runnermgr"
)

// LocalPairPort is where `setup` tells a browser on this computer which code
// it is waiting for, so the app can connect it with one click. Only the
// server's own origin may read it (CORS).
const LocalPairPort = 47641

const keychainService = "fleetpocket-node"

// setup walks through everything a computer needs and ends with the node
// running at login: Docker, agent keys (Keychain), pairing, the agent image.
func setup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	server := fs.String("server", os.Getenv("FLEETPOCKET_SERVER"), "Fleetpocket server URL")
	_ = fs.Parse(args)
	srv := strings.TrimRight(*server, "/")
	if srv == "" {
		return errors.New("set --server (the address of your Fleetpocket)")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	in := bufio.NewReader(os.Stdin)
	ui := &ui{in: in}

	ui.title("Fleetpocket: add this computer to " + srv)
	if runtime.GOOS != "darwin" {
		ui.note("This setup is made for macOS; on Linux it pairs the computer and prints how to run the node.")
	}

	// 1. Docker
	ui.step("Docker")
	dc, err := ensureDocker(ctx, ui)
	if err != nil {
		return err
	}
	ui.ok("Docker is running")

	// 2. Agent keys
	ui.step("Coding agents")
	secrets := map[string]string{}
	if err := askAgents(ctx, ui, secrets); err != nil {
		return err
	}
	agents := agentsWith(secrets)
	if len(agents) == 0 {
		ui.warn("No agent key yet: the computer connects, but tasks can run here only after you add one (run setup again).")
	} else {
		ui.ok("Agents: " + strings.Join(agents, ", "))
	}

	// 3. Pairing
	ui.step("Connect to your Fleetpocket")
	res, err := pair(ctx, ui, srv, agents)
	if err != nil {
		return err
	}
	secrets["FLEETPOCKET_NODE_TOKEN"] = res.Token
	if err := saveSecrets(secrets); err != nil {
		return err
	}
	ui.ok(fmt.Sprintf("Connected as %q", res.Name))

	// 4. Agent image, secrets scanner
	ui.step("Agent image")
	img := res.RunnerImage
	if o := os.Getenv("RUNNER_IMAGE_OVERRIDE"); o != "" {
		img = o
	}
	if img != "" {
		docker := &runnermgr.Manager{Docker: dc}
		if docker.HasImage(ctx, img) != nil {
			ui.note("Downloading " + img + " (a few minutes the first time)…")
			if err := docker.PullImage(ctx, img); err != nil {
				ui.warn("Could not download the image now (" + err.Error() + "); the first task downloads it.")
			} else {
				ui.ok("Image ready")
			}
		} else {
			ui.ok("Image ready")
		}
	}
	ensureGitleaks(ctx, ui)

	// 5. Start at login
	ui.step("Start")
	if runtime.GOOS == "darwin" {
		if err := installLaunchAgent(srv); err != nil {
			return err
		}
		ui.ok("Running now and at every login")
		home, _ := os.UserHomeDir()
		ui.note("Logs: " + filepath.Join(home, "Library", "Logs", "fleetpocket-node.log"))
	} else {
		ui.note("Run the node with these variables set (keep them out of files others can read):")
		fmt.Printf("\n  FLEETPOCKET_NODE_TOKEN=%s %s run --server %s\n\n", res.Token, exe(), srv)
	}
	ui.title("Done. " + res.Name + " shows as online in Settings → Computers.")
	return nil
}

// ---- terminal UI ----

type ui struct{ in *bufio.Reader }

func (u *ui) title(s string) { fmt.Printf("\n\033[1m%s\033[0m\n", s) }
func (u *ui) step(s string)  { fmt.Printf("\n\033[1;34m▸ %s\033[0m\n", s) }
func (u *ui) ok(s string)    { fmt.Printf("  \033[32m✓\033[0m %s\n", s) }
func (u *ui) warn(s string)  { fmt.Printf("  \033[33m!\033[0m %s\n", s) }
func (u *ui) note(s string)  { fmt.Printf("  %s\n", s) }

// yes asks a yes/no question; def is the answer for a bare Enter.
func (u *ui) yes(q string, def bool) bool {
	hint := "[Y/n]"
	if !def {
		hint = "[y/N]"
	}
	fmt.Printf("  %s %s ", q, hint)
	line, _ := u.in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return def
	case "y", "yes", "т", "так":
		return true
	}
	return false
}

// hidden reads a secret without echo (falls back to a plain line).
func (u *ui) hidden(q string) string {
	fmt.Printf("  %s ", q)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	line, _ := u.in.ReadString('\n')
	return strings.TrimSpace(line)
}

// ---- Docker ----

func ensureDocker(ctx context.Context, u *ui) (*client.Client, error) {
	if dc := dockerUp(ctx); dc != nil {
		return dc, nil
	}
	u.warn("Docker is not running on this computer.")
	if runtime.GOOS == "darwin" {
		if hasApp("OrbStack") || hasApp("Docker") {
			u.note("Starting it…")
			if hasApp("OrbStack") {
				_ = exec.Command("open", "-a", "OrbStack").Run()
			} else {
				_ = exec.Command("open", "-a", "Docker").Run()
			}
		} else if _, err := exec.LookPath("brew"); err == nil && u.yes("Install OrbStack (a light Docker for Mac) with Homebrew?", true) {
			if err := runVisible(ctx, "brew", "install", "--cask", "orbstack"); err != nil {
				return nil, fmt.Errorf("installing OrbStack: %w", err)
			}
			_ = exec.Command("open", "-a", "OrbStack").Run()
		} else {
			u.note("Install OrbStack (https://orbstack.dev) or Docker Desktop, open it, and this setup continues by itself.")
		}
	} else {
		u.note("Install and start Docker (https://docs.docker.com/engine/install/); this setup continues by itself.")
	}
	u.note("Waiting for Docker…")
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
		if dc := dockerUp(ctx); dc != nil {
			return dc, nil
		}
	}
	return nil, errors.New("docker did not start; run setup again once it runs")
}

func dockerUp(ctx context.Context) *client.Client {
	dc, err := runnermgr.NewDockerClient(dockerHost())
	if err != nil {
		return nil
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if (&runnermgr.Manager{Docker: dc}).Ping(pctx) != nil {
		return nil
	}
	return dc
}

func hasApp(name string) bool {
	for _, dir := range []string{"/Applications", filepath.Join(os.Getenv("HOME"), "Applications")} {
		if _, err := os.Stat(filepath.Join(dir, name+".app")); err == nil {
			return true
		}
	}
	return false
}

func runVisible(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func ensureGitleaks(ctx context.Context, u *ui) {
	if _, err := exec.LookPath("gitleaks"); err == nil {
		u.ok("Secrets scanner (gitleaks) found")
		return
	}
	if _, err := exec.LookPath("brew"); err == nil && u.yes("Install gitleaks (scans changes for leaked secrets before a PR; your server may require it)?", true) {
		if err := runVisible(ctx, "brew", "install", "gitleaks"); err == nil {
			u.ok("gitleaks installed")
			return
		}
	}
	u.warn("Without gitleaks, PRs from this computer fail if the server requires the secrets scan (SECRETS_SCAN=gitleaks).")
}

// ---- agents ----

func askAgents(ctx context.Context, u *ui, out map[string]string) error {
	if secret("CLAUDE_CODE_OAUTH_TOKEN") != "" || secret("ANTHROPIC_API_KEY") != "" {
		u.ok("Claude: key already in the Keychain")
	} else if u.yes("Connect Claude (your Claude subscription)?", true) {
		if _, err := exec.LookPath("claude"); err == nil {
			u.note("Opening `claude setup-token`: approve it in the browser, then copy the token it prints.")
			if err := runVisible(ctx, "claude", "setup-token"); err != nil {
				u.warn("claude setup-token did not finish: " + err.Error())
			}
		} else {
			u.note("Claude Code is not installed here. On any computer with it, run `claude setup-token` and copy the token (or use an Anthropic API key).")
		}
		if tok := u.hidden("Paste the token (Enter to skip):"); tok != "" {
			if strings.HasPrefix(tok, "sk-ant-api") {
				out["ANTHROPIC_API_KEY"] = tok
			} else {
				out["CLAUDE_CODE_OAUTH_TOKEN"] = tok
			}
		}
	}
	if secret("OPENAI_API_KEY") != "" {
		u.ok("Codex: key already in the Keychain")
	} else if u.yes("Add Codex (an OpenAI API key)?", false) {
		if k := u.hidden("OpenAI API key:"); k != "" {
			out["OPENAI_API_KEY"] = k
		}
	}
	if secret("CURSOR_API_KEY") != "" {
		u.ok("Cursor: key already in the Keychain")
	} else if u.yes("Add Cursor (a Cursor API key)?", false) {
		if k := u.hidden("Cursor API key:"); k != "" {
			out["CURSOR_API_KEY"] = k
		}
	}
	return nil
}

// agentsWith lists the agents that have a key, new or already stored.
func agentsWith(newSecrets map[string]string) []string {
	has := func(names ...string) bool {
		for _, n := range names {
			if newSecrets[n] != "" || secret(n) != "" {
				return true
			}
		}
		return false
	}
	var out []string
	if has("CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY") {
		out = append(out, "claude")
	}
	if has("OPENAI_API_KEY") {
		out = append(out, "codex")
	}
	if has("CURSOR_API_KEY") {
		out = append(out, "cursor")
	}
	return out
}

// saveSecrets writes to the macOS Keychain (elsewhere they are only used by
// this process and printed for the user to keep).
func saveSecrets(m map[string]string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	for name, v := range m {
		if v == "" {
			continue
		}
		cmd := exec.Command("security", "add-generic-password", "-U", "-s", keychainService, "-a", name, "-w", v)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("saving %s in the Keychain: %v: %s", name, err, bytes.TrimSpace(out))
		}
	}
	return nil
}

// ---- pairing ----

type pairStart struct {
	Code       string `json:"code"`
	PollID     string `json:"poll_id"`
	ExpiresIn  int    `json:"expires_in"`
	ApproveURL string `json:"approve_url"`
}

type pairResult struct {
	Status      string `json:"status"`
	NodeID      string `json:"node_id"`
	Name        string `json:"name"`
	Token       string `json:"token"`
	RunnerImage string `json:"runner_image"`
}

func pair(ctx context.Context, u *ui, srv string, agents []string) (*pairResult, error) {
	host := computerName()
	body, _ := json.Marshal(map[string]any{"hostname": host, "platform": runtime.GOOS + "/" + runtime.GOARCH, "agents": nonNilStrings(agents)})
	var start pairStart
	if err := apiCall(ctx, http.MethodPost, srv+"/api/v1/node/pair", body, &start); err != nil {
		return nil, fmt.Errorf("asking %s for a pairing code: %w", srv, err)
	}

	stopLocal := serveLocalPairing(srv, start.Code, host)
	defer stopLocal()

	fmt.Printf("\n    Pairing code:  \033[1m%s\033[0m\n\n", start.Code)
	u.note("In Fleetpocket open Settings → Computers: on this computer it offers \"Connect " + host + "\" (one click);")
	u.note("on another device, enter the code there. Opening it in your browser…")
	if runtime.GOOS == "darwin" && start.ApproveURL != "" {
		_ = exec.Command("open", start.ApproveURL).Start()
	}
	u.note("Waiting for you to connect it (" + fmt.Sprint(start.ExpiresIn/60) + " minutes)…")

	deadline := time.Now().Add(time.Duration(start.ExpiresIn) * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
		var res pairResult
		err := apiCall(ctx, http.MethodGet, srv+"/api/v1/node/pair/"+url.PathEscape(start.PollID), nil, &res)
		if err != nil {
			var he *httpError
			if errors.As(err, &he) && he.status == http.StatusNotFound {
				break
			}
			continue // network blip: keep waiting
		}
		if res.Status == "approved" && res.Token != "" {
			return &res, nil
		}
	}
	return nil, errors.New("the code expired before it was connected; run setup again")
}

type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.status, e.body) }

func apiCall(ctx context.Context, method, u string, body []byte, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "fleetpocket")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return &httpError{status: resp.StatusCode, body: strings.TrimSpace(string(b))}
	}
	return json.Unmarshal(b, out)
}

// serveLocalPairing answers GET http://127.0.0.1:47641/pairing with the code,
// readable only by pages of the Fleetpocket origin, so the app open on this
// computer can connect it with one click. Returns a stop function.
func serveLocalPairing(srv, code, host string) func() {
	origin := srv
	if u, err := url.Parse(srv); err == nil {
		origin = u.Scheme + "://" + u.Host
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", LocalPairPort))
	if err != nil {
		return func() {}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/pairing", func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		if r.Header.Get("Origin") != origin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Vary", "Origin")
		// Chrome's Private Network Access preflight for a public page.
		h.Set("Access-Control-Allow-Private-Network", "true")
		h.Set("Access-Control-Allow-Methods", "GET")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.Set("Content-Type", "application/json")
		h.Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "hostname": host, "platform": runtime.GOOS + "/" + runtime.GOARCH})
	})
	hs := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = hs.Serve(ln) }()
	return func() { _ = hs.Close() }
}

// computerName is the Mac's friendly name ("Andrii's MacBook Pro"), else the host name.
func computerName() string {
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("scutil", "--get", "ComputerName").Output(); err == nil {
			if n := strings.TrimSpace(string(out)); n != "" {
				return n
			}
		}
	}
	h, _ := os.Hostname()
	return h
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ---- launchd ----

var plistTmpl = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- Written by fleetpocket-node setup. Secrets are in the Keychain, not here. -->
<plist version="1.0">
<dict>
  <key>Label</key><string>com.fleetpocket.node</string>
  <key>ProgramArguments</key>
  <array><string>{{.Exe}}</string><string>run</string><string>--server</string><string>{{.Server}}</string></array>
  <key>EnvironmentVariables</key>
  <dict><key>PATH</key><string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardOutPath</key><string>{{.Log}}</string>
  <key>StandardErrorPath</key><string>{{.Log}}</string>
</dict>
</plist>
`))

func installLaunchAgent(srv string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(home, "Library", "Logs"), 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "com.fleetpocket.node.plist")
	var b bytes.Buffer
	esc := func(s string) string {
		var x bytes.Buffer
		_ = xml.EscapeText(&x, []byte(s))
		return x.String()
	}
	if err := plistTmpl.Execute(&b, map[string]string{
		"Exe": esc(exe()), "Server": esc(srv), "Log": esc(filepath.Join(home, "Library", "Logs", "fleetpocket-node.log")),
	}); err != nil {
		return err
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		return err
	}
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	// Replace a running copy (setup run again, or a new version).
	_ = exec.Command("launchctl", "bootout", domain+"/com.fleetpocket.node").Run()
	if out, err := exec.Command("launchctl", "bootstrap", domain, path).CombinedOutput(); err != nil {
		return fmt.Errorf("starting the node with launchd: %v: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

func exe() string {
	p, err := os.Executable()
	if err != nil {
		return "fleetpocket-node"
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
