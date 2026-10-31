package httpapi

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type credential struct {
	UID, Login, Token string
	Expires           time.Time
}
type oauthFlow struct {
	State, Verifier string
	Expires         time.Time
}

func randomID() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func seal(key []byte, name string, v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(gcm.Seal(nonce, nonce, b, []byte(name))), nil
}
func unseal(key []byte, name, value string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	if len(raw) < gcm.NonceSize() {
		return errors.New("invalid_cookie")
	}
	b, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte(name))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
func (s *Server) cookie(w http.ResponseWriter, name string, v any, age int) error {
	value := ""
	var err error
	if age > 0 {
		value, err = seal(s.Config.CookieKey, name, v)
		if err != nil {
			return err
		}
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: !s.Config.Local, SameSite: http.SameSiteLaxMode, MaxAge: age})
	return nil
}
func (s *Server) credential(r *http.Request) (credential, error) {
	var c credential
	cookie, err := r.Cookie("factory_github")
	if err != nil {
		return c, err
	}
	err = unseal(s.Config.CookieKey, "factory_github", cookie.Value, &c)
	if err == nil && (c.UID == "" || c.Token == "" || time.Now().After(c.Expires)) {
		err = errors.New("github_login_expired")
	}
	return c, err
}
func (s *Server) identity(ctx context.Context, r *http.Request, bearer string) (credential, error) {
	c, err := s.credential(r)
	if err != nil {
		return c, err
	}
	if bearer == "" {
		bearer = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if bearer == "" {
		return c, errors.New("unauthorized")
	}
	token, err := s.Auth.VerifyIDTokenAndCheckRevoked(ctx, bearer)
	if err != nil || token.UID != c.UID {
		return c, errors.New("unauthorized")
	}
	return c, nil
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.Config.Demo {
		http.Redirect(w, r, s.Config.Origin+"/?login=demo", http.StatusSeeOther)
		return
	}
	f := oauthFlow{State: randomID(), Verifier: randomID() + randomID(), Expires: time.Now().Add(10 * time.Minute)}
	if err := s.cookie(w, "factory_oauth", f, 600); err != nil {
		fail(w, 500, "auth_unavailable")
		return
	}
	hash := sha256.Sum256([]byte(f.Verifier))
	q := url.Values{"client_id": {s.Config.ClientID}, "redirect_uri": {s.Config.Origin + "/auth/callback"}, "state": {f.State}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "code_challenge_method": {"S256"}}
	http.Redirect(w, r, "https://github.com/login/oauth/authorize?"+q.Encode(), http.StatusSeeOther)
}
func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	s.cookie(w, "factory_github", nil, -1)
	cookie, err := r.Cookie("factory_oauth")
	var f oauthFlow
	if err != nil || unseal(s.Config.CookieKey, "factory_oauth", cookie.Value, &f) != nil || time.Now().After(f.Expires) || subtle.ConstantTimeCompare([]byte(f.State), []byte(r.URL.Query().Get("state"))) != 1 || r.URL.Query().Get("code") == "" {
		fail(w, 400, "invalid_oauth_state")
		return
	}
	s.cookie(w, "factory_oauth", nil, -1)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	b, _ := json.Marshal(map[string]string{"client_id": s.Config.ClientID, "client_secret": s.Config.ClientSecret, "code": r.URL.Query().Get("code"), "code_verifier": f.Verifier, "redirect_uri": s.Config.Origin + "/auth/callback"})
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://github.com/login/oauth/access_token", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		fail(w, 502, "github_oauth_failed")
		return
	}
	defer resp.Body.Close()
	var token struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 16*1024)).Decode(&token) != nil || !strings.HasPrefix(token.AccessToken, "ghu_") {
		fail(w, 502, "github_oauth_failed")
		return
	}
	user, err := s.GitHub.User(ctx, token.AccessToken)
	if err != nil || user.ID < 1 {
		fail(w, 502, "github_identity_failed")
		return
	}
	lifetime := 8 * time.Hour
	if token.ExpiresIn > 0 && time.Duration(token.ExpiresIn)*time.Second < lifetime {
		lifetime = time.Duration(token.ExpiresIn) * time.Second
	}
	c := credential{UID: fmt.Sprintf("github:%d", user.ID), Login: user.Login, Token: token.AccessToken, Expires: time.Now().Add(lifetime)}
	if s.cookie(w, "factory_github", c, int(lifetime.Seconds())) != nil {
		fail(w, 500, "auth_unavailable")
		return
	}
	http.Redirect(w, r, s.Config.Origin+"/?login=complete", http.StatusSeeOther)
}
func (s *Server) complete(w http.ResponseWriter, r *http.Request) {
	var c credential
	var err error
	if s.Config.Demo {
		c = credential{UID: "github:1", Login: "local-developer", Token: "demo-github", Expires: time.Now().Add(8 * time.Hour)}
		err = s.cookie(w, "factory_github", c, 8*3600)
	} else {
		c, err = s.credential(r)
	}
	if err != nil {
		fail(w, 401, "github_login_required")
		return
	}
	token, err := s.Auth.CustomToken(r.Context(), c.UID)
	if err != nil {
		fail(w, 503, "firebase_auth_unavailable")
		return
	}
	reply(w, 200, map[string]string{"customToken": token, "login": c.Login})
}
