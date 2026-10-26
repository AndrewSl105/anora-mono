package config

import (
	"strings"
	"testing"
)

func validConfig() *Config {
	return &Config{
		DatabaseURL:       "postgres://db",
		AdminPasswordHash: "$2a$12$test",
		AdminTOTPSecret:   "totp",
		SessionSecret:     "session",
		WorkspacesDir:     "/srv/fleetpocket/workspaces",
		AttachmentsDir:    "/srv/fleetpocket/attachments",
		SecretsScan:       "off",
		STTProvider:       "off",
	}
}

func TestValidateSpeechToTextProvider(t *testing.T) {
	tests := []struct {
		name, provider, key, url, want string
	}{
		{name: "off", provider: "off"},
		{name: "openai", provider: "openai", key: "secret"},
		{name: "openai key required", provider: "openai", want: "OPENAI_API_KEY is required"},
		{name: "whispercpp", provider: "whispercpp", url: "http://whispercpp:8080"},
		{name: "whispercpp url required", provider: "whispercpp", want: "WHISPERCPP_URL is required"},
		{name: "unknown", provider: "other", want: `STT_PROVIDER must be "off", "openai", or "whispercpp"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.STTProvider, cfg.OpenAIAPIKey, cfg.WhisperCPPURL = tt.provider, tt.key, tt.url
			err := cfg.Validate()
			if tt.want == "" && err != nil {
				t.Fatal(err)
			}
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("Validate() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestAgents(t *testing.T) {
	c := &Config{ClaudeOAuthToken: "oauth", AnthropicAPIKey: "api", CursorAPIKey: "cur"}
	if got := c.AgentEnv("claude"); len(got) != 1 || got["CLAUDE_CODE_OAUTH_TOKEN"] != "oauth" {
		t.Fatalf("claude env: %v", got)
	}
	if got := c.AgentEnv("cursor"); len(got) != 1 || got["CURSOR_API_KEY"] != "cur" {
		t.Fatalf("cursor env: %v", got)
	}
	if got := c.AgentEnv("codex"); len(got) != 0 {
		t.Fatalf("codex env without key: %v", got)
	}
	if got := strings.Join(c.EnabledAgents(), ","); got != "claude,cursor" {
		t.Fatalf("enabled: %s", got)
	}
	c.RunnerFake = true
	if got := strings.Join(c.EnabledAgents(), ","); got != "claude,codex,cursor" || c.AgentEnabled("gemini") {
		t.Fatalf("fake enabled: %s", got)
	}

	t.Setenv("DEFAULT_AGENT", "gemini")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DEFAULT_AGENT") {
		t.Fatalf("bad DEFAULT_AGENT: %v", err)
	}
	t.Setenv("DEFAULT_AGENT", "codex")
	if c, err := Load(); err != nil || c.DefaultAgent != "codex" {
		t.Fatalf("DEFAULT_AGENT: %v %v", c, err)
	}
}
