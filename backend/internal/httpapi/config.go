package httpapi

import (
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"regexp"
	"strings"
)

type Config struct {
	Local                                                  bool
	Demo                                                   bool
	Project, Origin, Port, ClientID, ClientSecret, AppSlug string
	CookieKey                                              []byte
	Models                                                 []string
	AIProvider                                             string
	VertexProject, VertexRegion                            string
}

// DefaultGeminiModels lists the Gemini models offered when GEMINI_MODELS is
// unset. The first entry is selected by default in the UI.
var DefaultGeminiModels = []string{"gemini-3.5-flash-lite", "gemini-3.8-flash", "gemini-3.1-pro-preview"}

// DefaultClaudeModels lists the Claude models on Vertex AI offered when
// CLAUDE_MODELS is unset. The first entry is selected by default in the UI.
var DefaultClaudeModels = []string{"claude-haiku-5-5", "claude-sonnet-5-5"}

// splitModels parses a comma-separated allowlist, ignoring spaces around entries.
func splitModels(value string) []string {
	models := strings.Split(value, ",")
	for i, model := range models {
		models[i] = strings.TrimSpace(model)
	}
	return models
}

func LoadConfig() (Config, error) {
	c := Config{Local: os.Getenv("APP_ENV") == "local", Demo: os.Getenv("PROVIDER") == "demo", Project: os.Getenv("GCP_PROJECT_ID"), Origin: os.Getenv("WEB_ORIGIN"), Port: os.Getenv("PORT"), ClientID: os.Getenv("GITHUB_CLIENT_ID"), ClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"), AppSlug: os.Getenv("GITHUB_APP_SLUG"), Models: []string{"gpt-5-mini"}}
	c.AIProvider = os.Getenv("AI_PROVIDER")
	if c.AIProvider == "" {
		if c.Demo {
			c.AIProvider = "demo"
		} else {
			c.AIProvider = "openai"
		}
	}
	if c.AIProvider != "demo" && c.AIProvider != "openai" && c.AIProvider != "gemini" && c.AIProvider != "vertex" {
		return c, errors.New("invalid AI_PROVIDER")
	}
	if !c.Demo && c.AIProvider == "demo" {
		return c, errors.New("demo AI requires demo provider")
	}
	if c.AIProvider == "gemini" {
		c.Models = DefaultGeminiModels
		if value := os.Getenv("GEMINI_MODELS"); value != "" {
			c.Models = splitModels(value)
		}
	}
	if c.AIProvider == "vertex" {
		c.Models = DefaultClaudeModels
		if value := os.Getenv("CLAUDE_MODELS"); value != "" {
			c.Models = splitModels(value)
		}
		c.VertexProject, c.VertexRegion = os.Getenv("VERTEX_PROJECT_ID"), os.Getenv("VERTEX_REGION")
		if c.VertexRegion == "" {
			c.VertexRegion = "global"
		}
		if !regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`).MatchString(c.VertexProject) {
			return c, errors.New("VERTEX_PROJECT_ID required")
		}
		if !regexp.MustCompile(`^[a-z0-9-]+$`).MatchString(c.VertexRegion) {
			return c, errors.New("invalid VERTEX_REGION")
		}
	}
	if c.Port == "" {
		c.Port = "8080"
	}
	if value := os.Getenv("OPENAI_MODELS"); value != "" && c.AIProvider == "openai" {
		c.Models = splitModels(value)
	}
	for _, model := range c.Models {
		if c.AIProvider == "gemini" && !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`).MatchString(model) {
			return c, errors.New("invalid Gemini model allowlist")
		}
		if c.AIProvider == "vertex" && !regexp.MustCompile(`^claude-[a-z0-9.-]+(@[0-9]{8})?$`).MatchString(model) {
			return c, errors.New("invalid Claude model allowlist")
		}
		if strings.TrimSpace(model) != model || model == "" {
			return c, errors.New("invalid model allowlist")
		}
	}
	u, err := url.Parse(c.Origin)
	if err != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return c, errors.New("WEB_ORIGIN must be an origin")
	}
	if c.Project == "" {
		return c, errors.New("GCP_PROJECT_ID required")
	}
	if c.Local {
		if !strings.HasPrefix(c.Project, "demo-") || os.Getenv("FIRESTORE_EMULATOR_HOST") == "" || os.Getenv("FIREBASE_AUTH_EMULATOR_HOST") == "" || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
			return c, errors.New("local requires demo project, loopback origin and both emulators")
		}
	} else {
		if c.Demo || strings.HasPrefix(c.Project, "demo-") || os.Getenv("FIRESTORE_EMULATOR_HOST") != "" || os.Getenv("FIREBASE_AUTH_EMULATOR_HOST") != "" || u.Scheme != "https" {
			return c, errors.New("unsafe production configuration")
		}
	}
	key := os.Getenv("COOKIE_KEY")
	if c.Local && key == "" {
		key = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	}
	c.CookieKey, err = base64.StdEncoding.DecodeString(key)
	if err != nil || len(c.CookieKey) != 32 {
		return c, errors.New("COOKIE_KEY must be base64-encoded 32 random bytes")
	}
	if !c.Demo && (c.ClientID == "" || c.ClientSecret == "" || c.AppSlug == "") {
		return c, errors.New("GitHub App credentials required")
	}
	return c, nil
}
