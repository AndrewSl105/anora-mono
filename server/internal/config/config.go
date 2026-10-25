// Package config reads Fleetpocket Runner settings from the environment.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr      string
	PublicBaseURL string
	DatabaseURL   string
	DockerHost    string
	WorkspacesDir string
	// AttachmentsDir holds images attached to tasks, one folder per task;
	// like WorkspacesDir, the same path on the host and in the container.
	AttachmentsDir string
	RunnerImage    string

	ClaudeOAuthToken string
	AnthropicAPIKey  string

	GitHubAppID          int64
	GitHubPrivateKeyPath string
	GitHubWebhookSecret  string
	// GitHubAPIURL is only overridden in tests.
	GitHubAPIURL string

	TelegramBotToken      string
	TelegramAllowedUserID int64
	WebAppURL             string
	// TelegramMiniAppLink is the optional t.me/<bot>/<app> link for deep links in groups.
	TelegramMiniAppLink string
	// STTProvider is "off", "openai", or "whispercpp". OpenAI is called
	// directly; whispercpp uses the optional HTTP sidecar protocol.
	STTProvider   string
	OpenAIAPIKey  string
	WhisperCPPURL string

	AdminPasswordHash string
	AdminTOTPSecret   string
	SessionSecret     string
	CORSOrigins       []string
	// CookieInsecure drops the Secure flag for local http development.
	CookieInsecure bool

	MaxConcurrentTasks int
	RunnerMemory       string
	RunnerCPUs         float64
	SidecarMemory      string
	// RunnerUser is the uid:gid runner containers run as (tests on hosts whose
	// user is not uid 1000 override it).
	RunnerUser string

	DefaultModel        string
	DefaultMaxTurns     int
	DefaultMaxBudgetUSD float64
	DefaultMaxDuration  time.Duration
	QuestionTimeout     time.Duration
	SidecarIdleTTL      time.Duration
	EventRetention      time.Duration
	WorkspaceRetention  time.Duration

	// SecretsScan is "gitleaks" (default) or "off" (local development only).
	SecretsScan string

	// RunnerFake starts runners with FLEETPOCKET_RUNNER_FAKE=1 (tests and local dev).
	RunnerFake bool
}

// Load reads the environment. Only settings needed by every command are
// required here; Validate checks what `serve` needs.
func Load() (*Config, error) {
	var errs []error
	e := envReader{errs: &errs}
	c := &Config{
		HTTPAddr:       e.str("HTTP_ADDR", ":8080"),
		PublicBaseURL:  strings.TrimRight(e.str("PUBLIC_BASE_URL", ""), "/"),
		DatabaseURL:    e.str("DATABASE_URL", ""),
		DockerHost:     e.str("DOCKER_HOST", "tcp://docker-proxy:2375"),
		WorkspacesDir:  e.str("WORKSPACES_DIR", "/srv/fleetpocket/workspaces"),
		AttachmentsDir: e.str("ATTACHMENTS_DIR", "/srv/fleetpocket/attachments"),
		RunnerImage:    e.str("RUNNER_IMAGE", "ghcr.io/andrewsl105/fleetpocket-agent:latest"),

		ClaudeOAuthToken: e.str("CLAUDE_CODE_OAUTH_TOKEN", ""),
		AnthropicAPIKey:  e.str("ANTHROPIC_API_KEY", ""),

		GitHubAppID:          e.int64("GITHUB_APP_ID", 0),
		GitHubPrivateKeyPath: e.str("GITHUB_APP_PRIVATE_KEY_PATH", ""),
		GitHubWebhookSecret:  e.str("GITHUB_WEBHOOK_SECRET", ""),
		GitHubAPIURL:         e.str("GITHUB_API_URL", ""),

		TelegramBotToken:      e.str("TELEGRAM_BOT_TOKEN", ""),
		TelegramAllowedUserID: e.int64("TELEGRAM_ALLOWED_USER_ID", 0),
		WebAppURL:             strings.TrimRight(e.str("WEB_APP_URL", ""), "/"),
		TelegramMiniAppLink:   e.str("TELEGRAM_MINIAPP_LINK", ""),
		STTProvider:           strings.ToLower(e.str("STT_PROVIDER", "off")),
		OpenAIAPIKey:          e.str("OPENAI_API_KEY", ""),
		WhisperCPPURL:         strings.TrimRight(e.str("WHISPERCPP_URL", ""), "/"),

		AdminPasswordHash: e.str("ADMIN_PASSWORD_HASH", ""),
		AdminTOTPSecret:   e.str("ADMIN_TOTP_SECRET", ""),
		SessionSecret:     e.str("SESSION_SECRET", ""),
		CORSOrigins:       splitList(e.str("CORS_ORIGINS", "")),
		CookieInsecure:    e.bool("COOKIE_INSECURE", false),

		MaxConcurrentTasks: e.int("MAX_CONCURRENT_TASKS", 2),
		RunnerMemory:       e.str("RUNNER_MEMORY", "3g"),
		RunnerCPUs:         e.float("RUNNER_CPUS", 2),
		SidecarMemory:      e.str("SIDECAR_MEMORY", "512m"),
		RunnerUser:         e.str("RUNNER_USER", "1000:1000"),

		DefaultModel:        e.str("DEFAULT_MODEL", ""),
		DefaultMaxTurns:     e.int("DEFAULT_MAX_TURNS", 150),
		DefaultMaxBudgetUSD: e.float("DEFAULT_MAX_BUDGET_USD", 5),
		DefaultMaxDuration:  e.dur("DEFAULT_MAX_DURATION", 2*time.Hour),
		QuestionTimeout:     e.dur("QUESTION_TIMEOUT", 20*time.Minute),
		SidecarIdleTTL:      e.dur("SIDECAR_IDLE_TTL", 30*time.Minute),
		EventRetention:      e.dur("EVENT_RETENTION", 720*time.Hour),
		WorkspaceRetention:  e.dur("WORKSPACE_RETENTION", 7*24*time.Hour),

		SecretsScan: e.str("SECRETS_SCAN", "gitleaks"),
		RunnerFake:  e.bool("FLEETPOCKET_RUNNER_FAKE", false),
	}
	if c.MaxConcurrentTasks < 1 {
		errs = append(errs, errors.New("MAX_CONCURRENT_TASKS must be >= 1"))
	}
	return c, errors.Join(errs...)
}

// Validate checks the settings `serve` cannot run without. GitHub and
// Telegram are optional so the service can start before they are set up.
func (c *Config) Validate() error {
	var errs []error
	req := func(name, v string) {
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
	}
	req("DATABASE_URL", c.DatabaseURL)
	req("ADMIN_PASSWORD_HASH", c.AdminPasswordHash)
	req("ADMIN_TOTP_SECRET", c.AdminTOTPSecret)
	req("SESSION_SECRET", c.SessionSecret)
	if !strings.HasPrefix(c.AdminPasswordHash, "$2") && c.AdminPasswordHash != "" {
		errs = append(errs, errors.New("ADMIN_PASSWORD_HASH must be a bcrypt hash (use `fleetpocket hash-password`)"))
	}
	if c.GitHubEnabled() && c.GitHubWebhookSecret == "" {
		errs = append(errs, errors.New("GITHUB_WEBHOOK_SECRET is required with GITHUB_APP_ID"))
	}
	if c.SecretsScan != "gitleaks" && c.SecretsScan != "off" {
		errs = append(errs, errors.New(`SECRETS_SCAN must be "gitleaks" or "off"`))
	}
	if within(c.AttachmentsDir, c.WorkspacesDir) || within(c.WorkspacesDir, c.AttachmentsDir) {
		errs = append(errs, errors.New("ATTACHMENTS_DIR and WORKSPACES_DIR must not contain each other"))
	}
	if c.TelegramBotToken != "" && c.TelegramAllowedUserID == 0 {
		errs = append(errs, errors.New("TELEGRAM_ALLOWED_USER_ID is required with TELEGRAM_BOT_TOKEN"))
	}
	switch c.STTProvider {
	case "off":
	case "openai":
		if c.OpenAIAPIKey == "" {
			errs = append(errs, errors.New("OPENAI_API_KEY is required with STT_PROVIDER=openai"))
		}
	case "whispercpp":
		if c.WhisperCPPURL == "" {
			errs = append(errs, errors.New("WHISPERCPP_URL is required with STT_PROVIDER=whispercpp"))
		}
	default:
		errs = append(errs, errors.New(`STT_PROVIDER must be "off", "openai", or "whispercpp"`))
	}
	return errors.Join(errs...)
}

// within reports whether path is dir or below it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

func (c *Config) GitHubEnabled() bool   { return c.GitHubAppID != 0 && c.GitHubPrivateKeyPath != "" }
func (c *Config) TelegramEnabled() bool { return c.TelegramBotToken != "" }

type envReader struct{ errs *[]error }

func (e envReader) str(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func (e envReader) int(k string, def int) int {
	v := e.str(k, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		*e.errs = append(*e.errs, fmt.Errorf("%s: %w", k, err))
		return def
	}
	return n
}

func (e envReader) int64(k string, def int64) int64 {
	v := e.str(k, "")
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		*e.errs = append(*e.errs, fmt.Errorf("%s: %w", k, err))
		return def
	}
	return n
}

func (e envReader) float(k string, def float64) float64 {
	v := e.str(k, "")
	if v == "" {
		return def
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		*e.errs = append(*e.errs, fmt.Errorf("%s: %w", k, err))
		return def
	}
	return n
}

func (e envReader) dur(k string, def time.Duration) time.Duration {
	v := e.str(k, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		*e.errs = append(*e.errs, fmt.Errorf("%s: %w", k, err))
		return def
	}
	return d
}

func (e envReader) bool(k string, def bool) bool {
	v := e.str(k, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		*e.errs = append(*e.errs, fmt.Errorf("%s: %w", k, err))
		return def
	}
	return b
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
