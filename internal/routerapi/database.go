package routerapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Backup map[string]any

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func New(baseURL string) (*Client, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "http://localhost:20128"
	}
	tok, err := CLIToken()
	if err != nil {
		return nil, err
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: tok, HTTP: &http.Client{Timeout: 30 * time.Second}}, nil
}

func NewWithToken(baseURL, token string) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "http://localhost:20128"
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

func CLIToken() (string, error) {
	base := os.Getenv("DATA_DIR")
	candidates := []string{}
	if base != "" {
		candidates = append(candidates, base)
	}
	if db := strings.TrimSpace(os.Getenv("NRTUI_DB")); db != "" {
		candidates = append(candidates, filepath.Dir(filepath.Dir(db)))
	}
	if home, _ := os.UserHomeDir(); home != "" {
		candidates = append(candidates, filepath.Join(home, ".9router"))
	}
	// Windows: %APPDATA%\9router
	if appdata := os.Getenv("APPDATA"); appdata != "" {
		candidates = append(candidates, filepath.Join(appdata, "9router"))
	}
	// Windows: %LOCALAPPDATA%\9router
	if localappdata := os.Getenv("LOCALAPPDATA"); localappdata != "" {
		candidates = append(candidates, filepath.Join(localappdata, "9router"))
	}
	// Linux fallback
	if runtime.GOOS != "windows" {
		candidates = append(candidates, "/home/hilman/.9router")
	}
	var last error
	for _, cand := range candidates {
		if cand == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(cand, "machine-id")); err == nil {
			base = cand
			break
		} else {
			last = err
		}
	}
	if base == "" {
		return "", fmt.Errorf("9Router data dir not found: %w", last)
	}
	mid, err := os.ReadFile(filepath.Join(base, "machine-id"))
	if err != nil {
		return "", fmt.Errorf("read 9Router machine-id: %w", err)
	}
	sec, err := os.ReadFile(filepath.Join(base, "auth", "cli-secret"))
	if err != nil {
		return "", fmt.Errorf("read 9Router cli-secret: %w", err)
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(string(mid)) + "9r-cli-auth" + strings.TrimSpace(string(sec))))
	return hex.EncodeToString(sum[:])[:16], nil
}

func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-9r-cli-token", c.Token)
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("9Router %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return b, nil
}

func (c *Client) ExportDatabase(ctx context.Context) (Backup, error) {
	b, err := c.do(ctx, http.MethodGet, "/api/settings/database", nil)
	if err != nil {
		return nil, err
	}
	var out Backup
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ImportDatabase(ctx context.Context, backup Backup) error {
	_, err := c.do(ctx, http.MethodPost, "/api/settings/database", backup)
	return err
}

func ProviderConnections(b Backup) []map[string]any {
	raw, _ := b["providerConnections"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		if m, ok := v.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func SetProviderConnections(b Backup, xs []map[string]any) {
	arr := make([]any, 0, len(xs))
	for _, x := range xs {
		arr = append(arr, x)
	}
	b["providerConnections"] = arr
}

func APIKeys(b Backup) []map[string]any {
	raw, _ := b["apiKeys"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		if m, ok := v.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func AccountID(m map[string]any) string { s, _ := m["id"].(string); return s }

func Bool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x != 0
	case int:
		return x != 0
	case string:
		return x == "true" || x == "1"
	default:
		return false
	}
}

func Clone(b Backup) (Backup, error) {
	bb, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	var out Backup
	if err := json.Unmarshal(bb, &out); err != nil {
		return nil, err
	}
	return out, nil
}
