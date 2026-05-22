package web

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Auth struct {
	EnvPath string
	mu      sync.Mutex
	sess    map[string]time.Time
}

func NewAuth(appDir string) *Auth {
	return &Auth{EnvPath: filepath.Join(appDir, ".env"), sess: map[string]time.Time{}}
}

func (a *Auth) EnsureEnv() error {
	if _, err := os.Stat(a.EnvPath); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(a.EnvPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(a.EnvPath, []byte("WEB_PASS=\n"), 0600)
}

func (a *Auth) EnsurePassword(prompt func() (string, error)) error {
	if err := a.EnsureEnv(); err != nil {
		return err
	}
	if a.password() != "" {
		return nil
	}
	pass, err := prompt()
	if err != nil {
		return err
	}
	pass = strings.TrimSpace(pass)
	if pass == "" {
		return fmt.Errorf("empty password")
	}
	return a.writePassword(pass)
}

func (a *Auth) password() string {
	f, err := os.Open(a.EnvPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if strings.HasPrefix(line, "WEB_PASS=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "WEB_PASS="))
		}
	}
	return ""
}

func (a *Auth) writePassword(pass string) error {
	if err := os.MkdirAll(filepath.Dir(a.EnvPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(a.EnvPath, []byte("WEB_PASS="+pass+"\n"), 0600)
}

func (a *Auth) CheckPassword(pass string) bool {
	stored := a.password()
	return stored != "" && pass == stored
}

func (a *Auth) Login(w http.ResponseWriter) error {
	tok, err := randomToken()
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.sess[tok] = time.Now().Add(24 * time.Hour)
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "9rtui_session", Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: time.Now().Add(24 * time.Hour)})
	return nil
}

func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("9rtui_session"); err == nil {
		a.mu.Lock()
		delete(a.sess, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "9rtui_session", Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
}

func (a *Auth) OK(r *http.Request) bool {
	c, err := r.Cookie("9rtui_session")
	if err != nil || c.Value == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.sess[c.Value]
	if !ok || time.Now().After(exp) {
		delete(a.sess, c.Value)
		return false
	}
	return true
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
