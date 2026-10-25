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
