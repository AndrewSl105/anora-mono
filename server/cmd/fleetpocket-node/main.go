// Command fleetpocket-node runs Fleetpocket tasks on this computer for a
// Fleetpocket server (docs/design/remote-nodes.md). It only makes outbound
// connections: a WebSocket to the server and the local Docker socket.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"

	"github.com/andrewsl105/fleetpocket/server/internal/hosts"
	"github.com/andrewsl105/fleetpocket/server/internal/nodeagent"
	"github.com/andrewsl105/fleetpocket/server/internal/runnermgr"
	"github.com/andrewsl105/fleetpocket/server/internal/workspace"
)

// version is set at build time (-ldflags "-X main.version=…").
var version = "dev"

const usage = `usage: fleetpocket-node <command> [flags]

commands:
  run        connect to the server and run tasks (default)
  doctor     check Docker, the runner image, credentials and gitleaks, then exit
  version    print the version

run / doctor flags:
  --server URL   the Fleetpocket server (FLEETPOCKET_SERVER), e.g. https://fleetpocket.example.com

Secrets are read from the environment, or on macOS from the Keychain
(service "fleetpocket-node", account = the variable name):
  FLEETPOCKET_NODE_TOKEN   the node token from Settings → Computers (required)
  CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY, OPENAI_API_KEY, CURSOR_API_KEY
Store one with:
  security add-generic-password -U -s fleetpocket-node -a FLEETPOCKET_NODE_TOKEN -w

Other settings: FLEETPOCKET_NODE_DIR (workspaces), DOCKER_HOST,
RUNNER_IMAGE_OVERRIDE, SIDECAR_MEMORY, SECRETS_SCAN=off.
`

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	cmd := "run"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "run":
		err = run(args, false)
	case "doctor":
		err = run(args, true)
	case "version":
		fmt.Println(version)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(args []string, doctor bool) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	server := fs.String("server", os.Getenv("FLEETPOCKET_SERVER"), "Fleetpocket server URL")
	_ = fs.Parse(args)
	if *server == "" {
		return errors.New("set --server or FLEETPOCKET_SERVER")
	}
	token := secret("FLEETPOCKET_NODE_TOKEN")
	if token == "" {
		return errors.New("no node token: set FLEETPOCKET_NODE_TOKEN or store it in the Keychain (see fleetpocket-node help)")
	}

	dir := os.Getenv("FLEETPOCKET_NODE_DIR")
	if dir == "" {
		dir = defaultDir()
	}
	ws := &workspace.Manager{Dir: filepath.Join(dir, "workspaces")}
	attDir := filepath.Join(dir, "attachments")
	for _, d := range []string{ws.Dir, attDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}

	dc, err := runnermgr.NewDockerClient(dockerHost())
	if err != nil {
		return err
	}
	docker := &runnermgr.Manager{Docker: dc, SidecarMemory: envOr("SIDECAR_MEMORY", "1g")}
	creds := credentials()
	local := &hosts.Local{
		Docker: docker, Workspace: ws, WriteAttachments: true,
		AttachmentsDir: func(id int64) string { return filepath.Join(attDir, fmt.Sprint(id)) },
		AgentEnv: func(agent string) map[string]string {
			out := map[string]string{}
			for k, v := range creds[agent] {
				out[k] = v
			}
			return out
		},
		// Docker Desktop / OrbStack resolve host.docker.internal to the Mac;
		// runners must not reach its local services by name.
		ExtraHosts: []string{"host.docker.internal:0.0.0.0", "gateway.docker.internal:0.0.0.0"},
	}
	if os.Getenv("SECRETS_SCAN") != "off" {
		if _, err := exec.LookPath("gitleaks"); err == nil {
			local.Scanner = workspace.Gitleaks("gitleaks")
		}
	}
	agents := []string{}
	for _, a := range []string{"claude", "codex", "cursor"} {
		if len(creds[a]) > 0 {
			agents = append(agents, a)
		}
	}
	if img := os.Getenv("RUNNER_IMAGE_OVERRIDE"); img != "" {
		local.ImageOverride = img
		slog.Warn("RUNNER_IMAGE_OVERRIDE is set; the server's image name is replaced", "image", img)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if doctor {
		return checkup(ctx, local, agents)
	}
	if err := local.Ping(ctx); err != nil {
		return fmt.Errorf("docker is not reachable (start Docker Desktop or OrbStack, or set DOCKER_HOST): %w", err)
	}
	slog.Info("fleetpocket-node", "version", version, "server", *server, "dir", dir, "agents", agents, "gitleaks", local.Scanner != nil)
	a := &nodeagent.Agent{
		Server: *server, Token: token, Version: version, Local: nodeagent.LocalHost{Local: local}, Agents: agents,
		KeepAwake: keepAwake(),
	}
	return a.Run(ctx)
}

func checkup(ctx context.Context, l *hosts.Local, agents []string) error {
	ok := true
	report := func(name string, err error, hint string) {
		if err != nil {
			ok = false
			fmt.Printf("✗ %s: %v\n  %s\n", name, err, hint)
		} else {
			fmt.Printf("✓ %s\n", name)
		}
	}
	report("docker", l.Ping(ctx), "start Docker Desktop or OrbStack, or set DOCKER_HOST")
	img := envOr("RUNNER_IMAGE_OVERRIDE", "ghcr.io/andrewsl105/fleetpocket-agent:latest")
	report("runner image "+img, l.HasImage(ctx, img), "docker pull "+img)
	var credErr error
	if len(agents) == 0 {
		credErr = errors.New("no agent credentials")
	}
	report(fmt.Sprintf("agents %v", agents), credErr, "set CLAUDE_CODE_OAUTH_TOKEN (claude setup-token), OPENAI_API_KEY or CURSOR_API_KEY")
	var scanErr error
	if l.Scanner == nil {
		scanErr = errors.New("gitleaks not found")
	}
	report("gitleaks", scanErr, "brew install gitleaks (needed when the server sets SECRETS_SCAN=gitleaks)")
	if !ok {
		return errors.New("some checks failed")
	}
	return nil
}

// credentials are this computer's agent keys, per agent (only that agent's
// runners get them).
func credentials() map[string]map[string]string {
	out := map[string]map[string]string{}
	if v := secret("CLAUDE_CODE_OAUTH_TOKEN"); v != "" {
		out["claude"] = map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": v}
	} else if v := secret("ANTHROPIC_API_KEY"); v != "" {
		out["claude"] = map[string]string{"ANTHROPIC_API_KEY": v}
	}
	if v := secret("OPENAI_API_KEY"); v != "" {
		out["codex"] = map[string]string{"OPENAI_API_KEY": v}
	}
	if v := secret("CURSOR_API_KEY"); v != "" {
		out["cursor"] = map[string]string{"CURSOR_API_KEY": v}
	}
	return out
}

// secret reads a variable from the environment, else (macOS) the Keychain.
func secret(name string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := exec.Command("security", "find-generic-password", "-s", "fleetpocket-node", "-a", name, "-w").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func defaultDir() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Fleetpocket")
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "fleetpocket")
	}
	return filepath.Join(home, ".local", "share", "fleetpocket")
}

// dockerHost is DOCKER_HOST, else the first socket that exists among Docker's
// default and the per-user sockets of Docker Desktop, OrbStack and Colima.
func dockerHost() string {
	if h := os.Getenv("DOCKER_HOST"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		"/var/run/docker.sock",
		filepath.Join(home, ".docker", "run", "docker.sock"),
		filepath.Join(home, ".orbstack", "run", "docker.sock"),
		filepath.Join(home, ".colima", "default", "docker.sock"),
	} {
		if _, err := os.Stat(p); err == nil {
			return "unix://" + p
		}
	}
	return ""
}

// keepAwake holds `caffeinate -i` (macOS) while runners are active, so the
// Mac does not idle-sleep in the middle of a task.
func keepAwake() func(bool) {
	if runtime.GOOS != "darwin" {
		return nil
	}
	var mu sync.Mutex
	var cmd *exec.Cmd
	return func(active bool) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case active && cmd == nil:
			c := exec.Command("caffeinate", "-i", "-w", fmt.Sprint(os.Getpid()))
			if err := c.Start(); err != nil {
				slog.Warn("caffeinate", "err", err)
				return
			}
			cmd = c
			go func() { _ = c.Wait() }()
		case !active && cmd != nil:
			_ = cmd.Process.Signal(syscall.SIGTERM)
			cmd = nil
		}
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
