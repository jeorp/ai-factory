package httpapi

import (
	"reflect"
	"testing"
)

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
