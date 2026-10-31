package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaticHostingAndAPIRoutesCoexist(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("workspace"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEB_DIR", dir)
	s := New(Config{Origin: "https://app.test"}, nil, nil, nil, nil, nil)
	handler := s.Handler()
	for _, path := range []string{"/", "/api/config", "/health/live"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("route %s failed: %d", path, w.Code)
		}
	}
}

func TestCookieEncryptionRejectsTamperingAndDifferentPurpose(t *testing.T) {
	key := []byte(strings.Repeat("a", 32))
	v, err := seal(key, "github", map[string]string{"token": "credential"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(v, "credential") {
		t.Fatal("plaintext cookie")
	}
	var decoded map[string]string
	if unseal(key, "github", v, &decoded) != nil || decoded["token"] != "credential" {
		t.Fatal("cookie roundtrip")
	}
	if unseal(key, "oauth", v, &decoded) == nil {
		t.Fatal("cookie purpose confused")
	}
	if unseal(key, "github", v[:len(v)-4]+"xxxx", &decoded) == nil {
		t.Fatal("tampered cookie accepted")
	}
}
func TestOriginAndSecretInputBoundary(t *testing.T) {
	s := New(Config{Origin: "https://app.test"}, nil, nil, nil, nil, nil)
	for _, origin := range []string{"", "https://attacker.test"} {
		r := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("CSRF allowed %q", origin)
		}
	}
	for _, text := range []string{"sk-this-is-a-secret-key-12345678", "github_pat_123456789012345", "ghu_123456789012345"} {
		if safeText(text) {
			t.Fatal("secret accepted in history")
		}
	}
}

func TestAIKeyValidationDoesNotAssumeProviderFormat(t *testing.T) {
	for _, key := range []string{"AIza-example-key", "different-format-key"} {
		if !validAIKey(key) {
			t.Fatal("valid key format rejected")
		}
	}
	for _, key := range []string{"", "with space", "with\nnewline", strings.Repeat("x", 513)} {
		if validAIKey(key) {
			t.Fatal("invalid key accepted")
		}
	}
}
