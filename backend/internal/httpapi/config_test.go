package httpapi

import (
	"reflect"
	"testing"
	"time"
)

func TestGeminiRequestTimeout(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{{"", 180 * time.Second}, {"240s", 4 * time.Minute}, {"1s", time.Second}, {"10m", 10 * time.Minute}, {"0", 0}, {"-1s", 0}, {"11m", 0}, {"180", 0}} {
		t.Run(tc.value, func(t *testing.T) {
			setLocalGeminiEnv(t)
			t.Setenv("GEMINI_REQUEST_TIMEOUT", tc.value)
			c, err := LoadConfig()
			if tc.want == 0 {
				if err == nil {
					t.Fatal("invalid timeout accepted")
				}
			} else if err != nil || c.GeminiRequestTimeout != tc.want {
				t.Fatalf("timeout=%v err=%v", c.GeminiRequestTimeout, err)
			}
		})
	}
}

func setLocalGeminiEnv(t *testing.T) {
	t.Setenv("APP_ENV", "local")
	t.Setenv("PROVIDER", "demo")
	t.Setenv("AI_PROVIDER", "gemini")
	t.Setenv("GCP_PROJECT_ID", "demo-test")
	t.Setenv("WEB_ORIGIN", "http://127.0.0.1:5175")
	t.Setenv("FIRESTORE_EMULATOR_HOST", "localhost:8085")
	t.Setenv("FIREBASE_AUTH_EMULATOR_HOST", "localhost:9099")
	t.Setenv("COOKIE_KEY", "")
}

func TestGeminiModelsDefaultIncludesProAndFlash(t *testing.T) {
	setLocalGeminiEnv(t)
	t.Setenv("GEMINI_MODELS", "")
	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Models, DefaultGeminiModels) || c.Models[0] != "gemini-3.5-flash-lite" {
		t.Fatalf("unexpected default models: %v", c.Models)
	}
}

func TestGeminiModelsAllowlistTrimsSpaces(t *testing.T) {
	setLocalGeminiEnv(t)
	t.Setenv("GEMINI_MODELS", "gemini-3.1-pro-preview, gemini-3.8-flash ,gemini-2.5-pro")
	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"gemini-3.1-pro-preview", "gemini-3.8-flash", "gemini-2.5-pro"}; !reflect.DeepEqual(c.Models, want) {
		t.Fatalf("got %v, want %v", c.Models, want)
	}
}

func TestGeminiModelsAllowlistRejectsInvalidEntries(t *testing.T) {
	for _, value := range []string{"gemini-3.8-flash,,gemini-3.1-pro-preview", "gemini/../x", "models/gemini-3.8-flash"} {
		setLocalGeminiEnv(t)
		t.Setenv("GEMINI_MODELS", value)
		if _, err := LoadConfig(); err == nil {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}

func TestVertexClaudeConfig(t *testing.T) {
	setLocalGeminiEnv(t)
	t.Setenv("AI_PROVIDER", "vertex")
	t.Setenv("CLAUDE_MODELS", "")
	t.Setenv("VERTEX_REGION", "")
	t.Setenv("VERTEX_PROJECT_ID", "")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected VERTEX_PROJECT_ID to be required")
	}
	t.Setenv("VERTEX_PROJECT_ID", "my-project-123")
	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"vertex/claude-haiku-5-5", "vertex/claude-sonnet-5-5"}; !reflect.DeepEqual(c.Models, want) || c.VertexRegion != "global" || c.VertexProject != "my-project-123" {
		t.Fatalf("unexpected config: %+v", c)
	}
	t.Setenv("CLAUDE_MODELS", "claude-haiku-4-5@20251001, claude-sonnet-5-5")
	if c, err = LoadConfig(); err != nil || c.Models[0] != "vertex/claude-haiku-4-5@20251001" {
		t.Fatalf("unexpected models: %v %v", c.Models, err)
	}
	t.Setenv("CLAUDE_MODELS", "gemini-3.8-flash")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected non-Claude model to be rejected")
	}
}

func TestGeminiAndVertexTogether(t *testing.T) {
	setLocalGeminiEnv(t)
	t.Setenv("AI_PROVIDER", "gemini,vertex,anthropic")
	t.Setenv("ANTHROPIC_MODELS", "")
	t.Setenv("GEMINI_MODELS", "gemini-3.8-flash")
	t.Setenv("CLAUDE_MODELS", "claude-haiku-5-5")
	t.Setenv("VERTEX_PROJECT_ID", "my-project-123")
	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"gemini-3.8-flash", "vertex/claude-haiku-5-5", "claude-haiku-5-5", "claude-sonnet-5-5"}; !reflect.DeepEqual(c.Models, want) || c.AIProvider != "gemini" {
		t.Fatalf("unexpected models: %v %s", c.Models, c.AIProvider)
	}
	if c.ModelProviders["gemini-3.8-flash"] != "gemini" || c.ModelProviders["vertex/claude-haiku-5-5"] != "vertex" || c.ModelProviders["claude-haiku-5-5"] != "anthropic" {
		t.Fatalf("unexpected model providers: %v", c.ModelProviders)
	}
	t.Setenv("AI_PROVIDER", "gemini,demo")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected demo to be rejected in a provider list")
	}
}

func TestAgentDebugIsLocalOptInOnly(t *testing.T) {
	for _, tc := range []struct {
		name, environment, flag, service string
		wantEnabled, wantError           bool
	}{
		{"default local off", "local", "", "", false, false},
		{"explicit local on", "local", "1", "", true, false},
		{"production rejected", "production", "1", "", false, true},
		{"cloud run rejected even with local env", "local", "1", "api-service", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setLocalGeminiEnv(t)
			t.Setenv("APP_ENV", tc.environment)
			t.Setenv("AGENT_DEBUG_LOG", tc.flag)
			t.Setenv("K_SERVICE", tc.service)
			c, err := LoadConfig()
			if (err != nil) != tc.wantError || c.AgentDebug != tc.wantEnabled {
				t.Fatalf("enabled=%v err=%v", c.AgentDebug, err)
			}
		})
	}
}
