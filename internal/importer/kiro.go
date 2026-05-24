package importer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/9rtui/9rtui/internal/routerapi"
	_ "modernc.org/sqlite"
)

const DefaultAPI = "http://192.168.0.44:20128"

type KiroAccount struct {
	ID           string         `json:"id,omitempty"`
	Provider     string         `json:"provider,omitempty"`
	AuthType     string         `json:"authType,omitempty"`
	Email        string         `json:"email,omitempty"`
	Status       string         `json:"status,omitempty"`
	RefreshToken string         `json:"refreshToken"`
	AccessToken  string         `json:"accessToken,omitempty"`
	Data         map[string]any `json:"data,omitempty"`
	RefreshHash  string         `json:"refreshHash,omitempty"`
	AccessHash   string         `json:"accessHash,omitempty"`
	ProfileARN   string         `json:"profileArn,omitempty"`
	ExpiresAt    string         `json:"expiresAt,omitempty"`
	LastUsedAt   string         `json:"lastUsedAt,omitempty"`
	PlanType     string         `json:"planType,omitempty"`
	CreditLimit  float64        `json:"creditLimit,omitempty"`
	Remaining    float64        `json:"remainingCredits,omitempty"`
	Available    bool           `json:"available"`
	Reason       string         `json:"reason,omitempty"`
	Raw          map[string]any `json:"-"`
}

type ImportOptions struct {
	AccountsPath    string
	APIBase         string
	DBPath          string
	DryRun          bool
	DoImport        bool
	ActiveOnly      bool
	IncludeInactive bool
	OnlyAvailable   bool
	IDs             []string
	Limit           int
	Sleep           time.Duration
	Parallel        int
	Progress        func(ImportResult)
	LogDir          string
	HTTPClient      *http.Client
	Now             func() time.Time
}

type Result struct {
	CreatedAt string         `json:"createdAt"`
	API       string         `json:"api"`
	Source    string         `json:"source"`
	Selected  int            `json:"selected"`
	Available int            `json:"available"`
	Skipped   int            `json:"skipped"`
	OK        int            `json:"ok"`
	Fail      int            `json:"fail"`
	LogPath   string         `json:"logPath,omitempty"`
	DBCheck   string         `json:"dbCheck,omitempty"`
	Rows      []KiroAccount  `json:"rows,omitempty"`
	Results   []ImportResult `json:"results,omitempty"`
}

type ImportResult struct {
	Email      string          `json:"email,omitempty"`
	SourceID   string          `json:"sourceId,omitempty"`
	HTTPStatus int             `json:"httpStatus"`
	Response   json.RawMessage `json:"response,omitempty"`
	Error      string          `json:"error,omitempty"`
}

func ManualOAuthProviders(ctx context.Context, base string) []string {
	providers := []string{}
	seen := map[string]bool{}
	if m, err := FetchExistingProviders(ctx, http.DefaultClient, firstNonEmptyString(base, DefaultAPI)); err == nil {
		for k := range m {
			p := strings.TrimSpace(strings.TrimPrefix(k, "provider:"))
			if p == "" || strings.Contains(p, ":") || seen[p] {
				continue
			}
			seen[p] = true
			providers = append(providers, p)
		}
	}
	for _, p := range []string{"claude-code", "antigravity", "codex", "github-copilot", "cursor", "xai", "kilo-code", "cline", "gemini-cli", "kiro"} {
		if !seen[p] {
			seen[p] = true
			providers = append(providers, p)
		}
	}
	sort.Strings(providers)
	return providers
}

func ImportManualRefreshTokens(ctx context.Context, opt ImportOptions, provider, text string) (Result, error) {
	opt = defaults(opt)
	provider = strings.ToLower(strings.TrimSpace(provider))
	rows, err := ParseManualRefreshTokens(provider, text)
	if err != nil {
		return Result{}, err
	}
	ex, _ := FetchExistingFromDB(opt.DBPath)
	markAvailability(rows, existingForProvider(ex, provider))
	res := Result{CreatedAt: opt.Now().Format(time.RFC3339), API: opt.APIBase, Source: "manual", Selected: len(rows), Rows: rows}
	for _, r := range rows {
		if r.Available {
			res.Available++
		} else {
			res.Skipped++
		}
	}
	if opt.DryRun || !opt.DoImport {
		return res, nil
	}
	results, err := importViaOfficialBackupAPI(ctx, opt, provider, rows)
	res.Results = append(res.Results, results...)
	for _, ir := range results {
		if ir.Error == "" {
			res.OK++
		} else {
			res.Fail++
		}
		if opt.Progress != nil {
			opt.Progress(ir)
		}
	}
	res.DBCheck = "manual official API import ok"
	return res, err
}

func ParseManualRefreshTokens(provider, text string) ([]KiroAccount, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" || provider == "manual" || provider == "all" {
		return nil, fmt.Errorf("manual import needs one provider")
	}
	var rows []KiroAccount
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("line %d: want name|refreshtoken", i+1)
		}
		name := strings.TrimSpace(parts[0])
		token := normalizeManualToken(parts[1])
		if name == "" {
			return nil, fmt.Errorf("line %d: name required", i+1)
		}
		if len(token) < 20 || strings.ContainsAny(token, " \t\r\n") {
			return nil, fmt.Errorf("line %d: malformed refresh token", i+1)
		}
		id := makeID(provider, name+":"+token)
		rows = append(rows, KiroAccount{ID: id, Provider: provider, AuthType: "oauth", Email: name, Status: "active", RefreshToken: token, Available: true, Data: map[string]any{"refreshToken": token, "authMethod": "manual", "provider": provider}})
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("manual import empty")
	}
	return rows, nil
}

func normalizeManualToken(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "'\"")
	s = strings.TrimPrefix(s, "refresh_token=")
	s = strings.TrimPrefix(s, "refreshToken=")
	return strings.TrimSpace(s)
}

func RunProvider(ctx context.Context, opt ImportOptions, provider string) (Result, error) {
	opt = defaults(opt)
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" || provider == "kiro" {
		return RunKiro(ctx, opt)
	}
	if err := ensureDevDBPath(opt.DBPath); err != nil {
		return Result{}, err
	}
	rows, err := LoadProviderAccounts(opt.AccountsPath, provider)
	if err != nil {
		return Result{}, err
	}
	if len(opt.IDs) > 0 {
		rows = filterIDs(rows, opt.IDs)
	}
	ex, _ := FetchExistingFromDB(opt.DBPath)
	markAvailability(rows, existingForProvider(ex, provider))
	if opt.OnlyAvailable {
		rows = filterAvailable(rows)
	}
	SortAccountsByProviderName(rows)
	if opt.Limit > 0 && opt.Limit < len(rows) {
		rows = rows[:opt.Limit]
	}
	res := Result{CreatedAt: opt.Now().Format(time.RFC3339), API: opt.APIBase, Source: opt.AccountsPath, Selected: len(rows), Rows: rows}
	for _, r := range rows {
		if r.Available {
			res.Available++
		} else {
			res.Skipped++
		}
	}
	if opt.DryRun || !opt.DoImport {
		return res, nil
	}
	results, err := importViaOfficialBackupAPI(ctx, opt, provider, rows)
	res.Results = append(res.Results, results...)
	for _, ir := range results {
		if ir.Error == "" {
			res.OK++
		} else {
			res.Fail++
		}
		if opt.Progress != nil {
			opt.Progress(ir)
		}
	}
	if err != nil {
		return res, err
	}
	res.DBCheck = "official API import ok"
	logPath, err := writeLog(opt.LogDir, opt.Now(), res)
	if err != nil {
		return res, err
	}
	res.LogPath = logPath
	_ = rewriteLog(logPath, res)
	return res, nil
}

func RunKiro(ctx context.Context, opt ImportOptions) (Result, error) {
	opt = defaults(opt)
	rows, err := LoadKiroAccounts(opt.AccountsPath)
	if err != nil {
		return Result{}, err
	}
	if opt.ActiveOnly && !opt.IncludeInactive {
		rows = filterActive(rows)
	}
	if len(opt.IDs) > 0 {
		rows = filterIDs(rows, opt.IDs)
	}
	client := opt.HTTPClient
	existing, err := FetchExistingProviders(ctx, client, opt.APIBase)
	if err != nil {
		return Result{}, err
	}
	if dbExisting, err := FetchExistingFromDB(opt.DBPath); err == nil {
		mergeExisting(existing, dbExisting)
	}
	markAvailability(rows, existing)
	if opt.OnlyAvailable {
		rows = filterAvailable(rows)
	}
	if opt.Limit > 0 && opt.Limit < len(rows) {
		rows = rows[:opt.Limit]
	}
	res := Result{CreatedAt: opt.Now().Format(time.RFC3339), API: opt.APIBase, Source: opt.AccountsPath, Selected: len(rows), Rows: rows}
	for _, r := range rows {
		if r.Available {
			res.Available++
		} else {
			res.Skipped++
		}
	}
	if opt.DryRun || !opt.DoImport {
		return res, nil
	}
	results, err := importViaOfficialBackupAPI(ctx, opt, "kiro", rows)
	res.Results = append(res.Results, results...)
	for _, ir := range results {
		if ir.Error == "" {
			res.OK++
		} else {
			res.Fail++
		}
		if opt.Progress != nil {
			opt.Progress(ir)
		}
	}
	if err != nil {
		return res, err
	}
	res.DBCheck = "official API import ok"
	logPath, err := writeLog(opt.LogDir, opt.Now(), res)
	if err != nil {
		return res, err
	}
	res.LogPath = logPath
	_ = rewriteLog(logPath, res)
	return res, nil
}

func LoadKiroAccounts(path string) ([]KiroAccount, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var root any
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, err
	}
	arr := rootArray(root)
	out := make([]KiroAccount, 0)
	counter := 0
	for _, v := range arr {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if str(m["provider"]) != "kiro" {
			continue
		}

		// Parse nested data field if stringified JSON
		var dataMap map[string]any
		if dataStr := str(m["data"]); dataStr != "" {
			_ = json.Unmarshal([]byte(dataStr), &dataMap)
		} else if dm, ok := m["data"].(map[string]any); ok {
			dataMap = dm
		}

		// Extract credentials from multiple possible locations
		creds, _ := m["credentials"].(map[string]any)

		// refreshToken priority: credentials.refresh_token > credentials.refreshToken > data.refreshToken > m.refreshToken
		rt := first(creds, "refresh_token", "refreshToken")
		if rt == "" && dataMap != nil {
			rt = first(dataMap, "refreshToken", "refresh_token")
		}
		if rt == "" {
			rt = str(m["refreshToken"])
		}
		if rt == "" {
			continue
		}

		// accessToken priority: credentials.access_token > credentials.accessToken > data.accessToken > m.accessToken
		at := first(creds, "access_token", "accessToken")
		if at == "" && dataMap != nil {
			at = first(dataMap, "accessToken", "access_token")
		}
		if at == "" {
			at = str(m["accessToken"])
		}

		// profileArn from multiple sources
		profileArn := first(creds, "profile_arn", "profileArn")
		if profileArn == "" && dataMap != nil {
			profileArn = first(dataMap, "profileArn", "profile_arn")
			if profileArn == "" {
				if psd, ok := dataMap["providerSpecificData"].(map[string]any); ok {
					profileArn = first(psd, "profileArn", "profile_arn")
				}
			}
		}
		if profileArn == "" {
			profileArn = str(m["profileArn"])
		}

		// expiresAt from multiple sources
		expiresAt := first(creds, "expires_at", "expiresAt")
		if expiresAt == "" && dataMap != nil {
			expiresAt = first(dataMap, "expiresAt", "expires_at")
		}
		if expiresAt == "" {
			expiresAt = str(m["expiresAt"])
		}

		// planType from multiple sources
		planType := first(creds, "plan_type", "planType")
		if planType == "" && dataMap != nil {
			planType = first(dataMap, "planType", "plan_type")
		}
		if planType == "" {
			planType = str(m["planType"])
		}

		// Email fallback: email > name > id > "Account N"
		email := str(m["email"])
		if email == "" {
			email = str(m["name"])
		}
		if email == "" {
			email = str(m["id"])
		}
		if email == "" {
			counter++
			email = fmt.Sprintf("Account %d", counter)
		}

		// Status: status > testStatus > isActive mapping
		status := str(m["status"])
		if status == "" && dataMap != nil {
			status = str(dataMap["testStatus"])
		}
		if status == "" {
			// Map isActive to status
			if isActive, ok := m["isActive"].(float64); ok && isActive == 1 {
				status = "active"
			} else if isActive == 0 {
				status = "inactive"
			}
		}

		out = append(out, KiroAccount{
			ID:           str(m["id"]),
			Email:        email,
			Status:       status,
			RefreshToken: rt,
			AccessToken:  at,
			RefreshHash:  tokenHash(rt),
			AccessHash:   tokenHash(at),
			ProfileARN:   profileArn,
			ExpiresAt:    expiresAt,
			LastUsedAt:   str(m["last_used_at"]),
			PlanType:     planType,
			CreditLimit:  num(m["credit_limit"]),
			Remaining:    num(m["remaining_credits"]),
		})
	}
	return out, nil
}

func FetchExistingProviders(ctx context.Context, client *http.Client, base string) (map[string]bool, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/api/providers", nil)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var v any
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, err
	}
	m := map[string]bool{}
	collectProviders(v, m)
	return m, nil
}

type existingKeys struct {
	email   map[string]bool
	profile map[string]bool
	refresh map[string]bool
	access  map[string]bool
}

func newExistingKeys() existingKeys {
	return existingKeys{email: map[string]bool{}, profile: map[string]bool{}, refresh: map[string]bool{}, access: map[string]bool{}}
}

func mergeExisting(dst map[string]bool, ex existingKeys) {
	for k := range ex.email {
		dst["email:"+k] = true
	}
	for k := range ex.profile {
		dst["profile:"+k] = true
	}
	for k := range ex.refresh {
		dst["refresh:"+k] = true
	}
	for k := range ex.access {
		dst["access:"+k] = true
	}
}

func FetchExistingFromDB(path string) (existingKeys, error) {
	ex := newExistingKeys()
	if strings.TrimSpace(path) == "" {
		path = os.Getenv("NRTUI_DB")
	}
	if strings.TrimSpace(path) == "" {
		path = default9RouterDBPath()
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return ex, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT COALESCE(email,''), data FROM providerConnections WHERE provider='kiro'`)
	if err != nil {
		return ex, err
	}
	defer rows.Close()
	for rows.Next() {
		var email, data string
		if err := rows.Scan(&email, &data); err != nil {
			return ex, err
		}
		if e := strings.ToLower(strings.TrimSpace(email)); e != "" {
			ex.email[e] = true
		}
		var m map[string]any
		if json.Unmarshal([]byte(data), &m) == nil {
			if h := tokenHash(str(m["refreshToken"])); h != "" {
				ex.refresh[h] = true
			}
			if h := tokenHash(str(m["accessToken"])); h != "" {
				ex.access[h] = true
			}
			if ps, ok := m["providerSpecificData"].(map[string]any); ok {
				if p := strings.ToLower(strings.TrimSpace(str(ps["profileArn"]))); p != "" {
					ex.profile[p] = true
				}
			}
		}
	}
	return ex, rows.Err()
}

func runImports(ctx context.Context, client *http.Client, opt ImportOptions, rows []KiroAccount, res Result) Result {
	workers := opt.Parallel
	if workers <= 0 {
		workers = 1
	}
	jobs := make(chan KiroAccount)
	results := make(chan ImportResult)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for row := range jobs {
				ir := postImport(ctx, client, opt.APIBase, row)
				results <- ir
				if opt.Sleep > 0 {
					select {
					case <-time.After(opt.Sleep):
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, row := range rows {
			if !row.Available {
				continue
			}
			select {
			case jobs <- row:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()
	for ir := range results {
		res.Results = append(res.Results, ir)
		if ir.HTTPStatus >= 200 && ir.HTTPStatus < 300 && ir.Error == "" {
			res.OK++
		} else {
			res.Fail++
		}
		if opt.Progress != nil {
			opt.Progress(ir)
		}
	}
	return res
}

func postImport(ctx context.Context, client *http.Client, base string, row KiroAccount) ImportResult {
	body, _ := json.Marshal(map[string]string{"refreshToken": row.RefreshToken})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/api/oauth/kiro/import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return ImportResult{Email: row.Email, SourceID: row.ID, Error: err.Error()}
	}
	defer resp.Body.Close()
	var raw json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&raw)
	ir := ImportResult{Email: row.Email, SourceID: row.ID, HTTPStatus: resp.StatusCode, Response: raw}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		ir.Error = http.StatusText(resp.StatusCode)
	}
	return ir
}

func defaults(o ImportOptions) ImportOptions {
	if o.AccountsPath == "" {
		o.AccountsPath = filepath.Join(".accounts", "accounts.json")
	}
	if o.APIBase == "" {
		if v := os.Getenv("NRTUI_API"); v != "" {
			o.APIBase = v
		} else {
			o.APIBase = DefaultAPI
		}
	}
	if o.LogDir == "" {
		if v := os.Getenv("NRTUI_LOG_DIR"); v != "" {
			o.LogDir = v
		} else {
			o.LogDir = "./.tui-logs"
		}
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if !o.IncludeInactive {
		o.ActiveOnly = true
	}
	return o
}
func rootArray(root any) []any {
	if a, ok := root.([]any); ok {
		return a
	}
	if m, ok := root.(map[string]any); ok {
		// Try multiple root array keys
		for _, key := range []string{"providerConnections", "accounts", "rows", "data"} {
			if a, ok := m[key].([]any); ok {
				return a
			}
		}
	}
	return nil
}
func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	default:
		return 0
	}
}
func tokenHash(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "***" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
func first(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := str(m[k]); s != "" {
			return s
		}
	}
	return ""
}
func filterActive(in []KiroAccount) []KiroAccount {
	out := in[:0]
	for _, r := range in {
		if r.Status == "active" {
			out = append(out, r)
		}
	}
	return out
}
func filterAvailable(in []KiroAccount) []KiroAccount {
	out := in[:0]
	for _, r := range in {
		if r.Available {
			out = append(out, r)
		}
	}
	return out
}
func filterIDs(in []KiroAccount, ids []string) []KiroAccount {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	out := in[:0]
	for _, r := range in {
		if want[r.ID] {
			out = append(out, r)
		}
	}
	return out
}
func markAvailability(rows []KiroAccount, existing map[string]bool) {
	seenRefresh := map[string]bool{}
	for i := range rows {
		refresh := rows[i].RefreshHash
		if rows[i].RefreshToken == "" || refresh == "" {
			rows[i].Available = false
			rows[i].Reason = "missing refresh token"
			continue
		}
		if existing["refresh:"+refresh] {
			rows[i].Available = false
			rows[i].Reason = "already exists: refresh"
			continue
		}
		if seenRefresh[refresh] {
			rows[i].Available = false
			rows[i].Reason = "duplicate source: refresh"
			continue
		}
		seenRefresh[refresh] = true
		rows[i].Available = true
		rows[i].Reason = ""
	}
}
func collectProviders(v any, out map[string]bool) {
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			collectProviders(e, out)
		}
	case map[string]any:
		if p := strings.ToLower(strings.TrimSpace(str(x["provider"]))); p != "" {
			out["provider:"+p] = true
			if p == "kiro" {
				if email := strings.ToLower(strings.TrimSpace(str(x["email"]))); email != "" {
					out["email:"+email] = true
				}
			}
		}
		for _, e := range x {
			collectProviders(e, out)
		}
	}
}
func writeLog(dir string, now time.Time, res Result) (string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, fmt.Sprintf("kiro-import-%s.json", now.Format("20060102-1504")))
	return p, rewriteLog(p, res)
}
func rewriteLog(path string, res Result) error {
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}
func LoadProviderAccounts(path, provider string) ([]KiroAccount, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if strings.HasSuffix(strings.ToLower(path), ".txt") {
		return loadProviderText(string(b), provider), nil
	}
	var root any
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, err
	}
	arr := rootArray(root)
	if len(arr) == 0 {
		if m, ok := root.(map[string]any); ok {
			arr = []any{m}
		}
	}
	out := make([]KiroAccount, 0, len(arr))
	allProviders := provider == "all"
	for i, v := range arr {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		rowProvider := strings.ToLower(strings.TrimSpace(str(m["provider"])))
		if rowProvider == "" {
			rowProvider = provider
		}
		if !allProviders && rowProvider != provider {
			continue
		}
		row := normalizeProviderRow(m, rowProvider, i+1)
		if row.RefreshToken == "" && row.AccessToken == "" {
			continue
		}
		out = append(out, row)
	}
	SortAccountsByProviderName(out)
	return out, nil
}

func SortAccountsByProviderName(rows []KiroAccount) {
	sort.SliceStable(rows, func(i, j int) bool {
		pi := strings.ToLower(strings.TrimSpace(rows[i].Provider))
		pj := strings.ToLower(strings.TrimSpace(rows[j].Provider))
		if pi != pj {
			return pi < pj
		}
		ni := strings.ToLower(firstNonEmptyString(rows[i].Email, rows[i].ID))
		nj := strings.ToLower(firstNonEmptyString(rows[j].Email, rows[j].ID))
		if ni != nj {
			return ni < nj
		}
		return rows[i].ID < rows[j].ID
	})
}

func loadProviderText(s, provider string) []KiroAccount {
	var out []KiroAccount
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(strings.ToLower(line), "refreshtoken") {
			continue
		}
		left, token, ok := strings.Cut(line, "|")
		if !ok {
			left, token = "", line
		}
		// New format: name/email|refreshtoken
		// Backward compat: name|refreshtoken
		var name, email string
		if n, e, hasSlash := strings.Cut(left, "/"); hasSlash {
			name = strings.TrimSpace(n)
			email = strings.TrimSpace(e)
		} else {
			name = strings.TrimSpace(left)
		}
		idx := len(out) + 1
		displayEmail := firstNonEmptyString(email, name, fmt.Sprintf("%s Account %d", importLabel(provider), idx))
		row := KiroAccount{Provider: provider, AuthType: "oauth", ID: makeID(provider, token), Email: displayEmail, RefreshToken: strings.TrimSpace(token), Status: "unavailable"}
		row.RefreshHash = tokenHash(row.RefreshToken)
		row.Data = oauthData(row)
		out = append(out, row)
	}
	return out
}

func normalizeProviderRow(m map[string]any, provider string, n int) KiroAccount {
	data := dataObject(m["data"])
	rt := firstDeep(m, "refreshToken", "refresh_token", "refresh-token", "rt")
	if rt == "" && data != nil {
		rt = firstDeep(data, "refreshToken", "refresh_token", "refresh-token", "rt")
	}
	at := firstDeep(m, "accessToken", "access_token", "access-token", "at")
	if at == "" && data != nil {
		at = firstDeep(data, "accessToken", "access_token", "access-token", "at")
	}
	status := firstDeep(m, "testStatus", "status")
	if status == "" && data != nil {
		status = firstDeep(data, "testStatus", "status")
	}
	if status == "" {
		status = "unavailable"
	}
	expiresAt := firstDeep(m, "expiresAt", "expires_at")
	if expiresAt == "" && data != nil {
		expiresAt = firstDeep(data, "expiresAt", "expires_at")
	}
	planType := firstDeep(m, "planType", "chatgptPlanType")
	if planType == "" && data != nil {
		planType = firstDeep(data, "planType", "chatgptPlanType")
	}
	email := firstNonEmptyString(str(m["email"]), str(m["name"]), str(m["id"]), fmt.Sprintf("%s Account %d", importLabel(provider), n))
	row := KiroAccount{ID: firstNonEmptyString(str(m["id"]), makeID(provider, rt+at)), Provider: firstNonEmptyString(str(m["provider"]), provider), AuthType: firstNonEmptyString(str(m["authType"]), "oauth"), Email: email, Status: status, RefreshToken: rt, AccessToken: at, RefreshHash: tokenHash(rt), AccessHash: tokenHash(at), ExpiresAt: expiresAt, PlanType: planType, Raw: cloneMap(m)}
	if data == nil {
		data = map[string]any{}
	}
	row.Data = data
	if row.RefreshToken != "" {
		row.Data["refreshToken"] = row.RefreshToken
	}
	if row.AccessToken != "" {
		row.Data["accessToken"] = row.AccessToken
	}
	if row.ExpiresAt != "" {
		row.Data["expiresAt"] = row.ExpiresAt
	} else if row.AccessToken == "" {
		row.Data["expiresAt"] = "1970-01-01T00:00:00Z"
	}
	if _, ok := row.Data["testStatus"]; !ok {
		row.Data["testStatus"] = row.Status
	}
	return row
}

func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	bb, err := json.Marshal(m)
	if err != nil {
		out := map[string]any{}
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	var out map[string]any
	if err := json.Unmarshal(bb, &out); err != nil {
		out = map[string]any{}
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func dataObject(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return cloneMap(m)
	}
	if s := str(v); s != "" {
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) == nil {
			return m
		}
	}
	return nil
}

func firstDeep(v any, keys ...string) string {
	want := map[string]bool{}
	for _, k := range keys {
		want[strings.ToLower(k)] = true
	}
	var walk func(any) string
	walk = func(x any) string {
		switch t := x.(type) {
		case map[string]any:
			for k, v := range t {
				if want[strings.ToLower(k)] {
					if s := str(v); s != "" {
						return s
					}
				}
			}
			for _, v := range t {
				if s := walk(v); s != "" {
					return s
				}
			}
		case []any:
			for _, v := range t {
				if s := walk(v); s != "" {
					return s
				}
			}
		}
		return ""
	}
	return walk(v)
}

func oauthData(row KiroAccount) map[string]any {
	m := map[string]any{"refreshToken": row.RefreshToken, "testStatus": row.Status}
	if row.AccessToken != "" {
		m["accessToken"] = row.AccessToken
	}
	if row.ExpiresAt != "" {
		m["expiresAt"] = row.ExpiresAt
	} else {
		m["expiresAt"] = "1970-01-01T00:00:00Z"
	}
	return m
}

func importViaOfficialBackupAPI(ctx context.Context, opt ImportOptions, provider string, rows []KiroAccount) ([]ImportResult, error) {
	client, err := routerapi.New(firstNonEmptyString(opt.APIBase, DefaultAPI))
	if err != nil {
		return nil, err
	}
	backup, err := client.ExportDatabase(ctx)
	if err != nil {
		return nil, err
	}
	conns := routerapi.ProviderConnections(backup)
	idx := map[string]int{}
	for i, c := range conns {
		idx[routerapi.AccountID(c)] = i
	}
	results := make([]ImportResult, 0, len(rows))
	now := opt.Now().Format(time.RFC3339)
	for _, row := range rows {
		if !row.Available {
			continue
		}
		rowProvider := firstNonEmptyString(row.Provider, provider)
		if rowProvider == "all" {
			rowProvider = ""
		}
		id := row.ID
		if id == "" {
			id = makeID(rowProvider, row.RefreshToken+row.AccessToken)
		}
		var m map[string]any
		if row.Raw != nil {
			m = cloneMap(row.Raw)
		} else {
			data := row.Data
			if data == nil {
				data = oauthData(row)
			}
			m = map[string]any{}
			for k, v := range data {
				m[k] = v
			}
			m["authType"] = firstNonEmptyString(row.AuthType, "oauth")
			m["name"] = row.Email
			m["email"] = row.Email
			m["priority"] = 0
			m["isActive"] = true
		}
		m["id"] = id
		m["provider"] = rowProvider
		if _, ok := m["authType"]; !ok {
			m["authType"] = firstNonEmptyString(row.AuthType, "oauth")
		}
		if _, ok := m["name"]; !ok {
			m["name"] = row.Email
		}
		if _, ok := m["email"]; !ok {
			m["email"] = row.Email
		}
		if _, ok := m["priority"]; !ok {
			m["priority"] = 0
		}
		if _, ok := m["isActive"]; !ok {
			m["isActive"] = true
		}
		if oldIdx, ok := idx[id]; ok {
			if s, _ := conns[oldIdx]["createdAt"].(string); s != "" {
				m["createdAt"] = s
			} else {
				m["createdAt"] = now
			}
			m["updatedAt"] = now
			conns[oldIdx] = m
		} else {
			m["createdAt"] = now
			m["updatedAt"] = now
			conns = append(conns, m)
			idx[id] = len(conns) - 1
		}
		results = append(results, ImportResult{Email: row.Email, SourceID: id, HTTPStatus: 200})
	}
	routerapi.SetProviderConnections(backup, conns)
	if err := client.ImportDatabase(ctx, backup); err != nil {
		return results, err
	}
	return results, nil
}

func upsertOAuthAccount(dbPath, provider string, row KiroAccount, now time.Time) ImportResult {
	if strings.TrimSpace(dbPath) == "" {
		dbPath = default9RouterDBPath()
	}
	if err := ensureDevDBPath(dbPath); err != nil {
		return ImportResult{Email: row.Email, SourceID: row.ID, Error: err.Error()}
	}
	db, err := sql.Open("sqlite", sqliteFileDSN(dbPath, "_busy_timeout=10000&_journal_mode=WAL&_foreign_keys=on"))
	if err != nil {
		return ImportResult{Email: row.Email, SourceID: row.ID, Error: err.Error()}
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	data := row.Data
	if data == nil {
		data = oauthData(row)
	}
	b, _ := json.Marshal(data)
	id := row.ID
	if id == "" {
		id = makeID(provider, row.RefreshToken+row.AccessToken)
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=10000; PRAGMA journal_mode=WAL;`); err != nil {
		return ImportResult{Email: row.Email, SourceID: id, Error: err.Error()}
	}
	tx, err := db.Begin()
	if err != nil {
		return ImportResult{Email: row.Email, SourceID: id, Error: err.Error()}
	}
	_, err = tx.Exec(`INSERT INTO providerConnections (id,provider,authType,name,email,priority,isActive,data,createdAt,updatedAt) VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,email=excluded.email,isActive=excluded.isActive,data=excluded.data,updatedAt=excluded.updatedAt`, id, provider, "oauth", row.Email, row.Email, 0, 1, string(b), now.Format(time.RFC3339), now.Format(time.RFC3339))
	if err != nil {
		_ = tx.Rollback()
		return ImportResult{Email: row.Email, SourceID: id, Error: err.Error()}
	}
	if err := tx.Commit(); err != nil {
		return ImportResult{Email: row.Email, SourceID: id, Error: err.Error()}
	}
	return ImportResult{Email: row.Email, SourceID: id, HTTPStatus: 200}
}

func quickCheckPath(dbPath string) (string, error) {
	if strings.TrimSpace(dbPath) == "" {
		dbPath = default9RouterDBPath()
	}
	if err := ensureDevDBPath(dbPath); err != nil {
		return "", err
	}
	db, err := sql.Open("sqlite", sqliteFileDSN(dbPath, "_busy_timeout=10000&_journal_mode=WAL&_foreign_keys=on"))
	if err != nil {
		return "", err
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	for _, q := range []string{
		`SELECT COUNT(*) FROM providerConnections`,
		`SELECT COUNT(*) FROM providerNodes`,
	} {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			return "", err
		}
	}
	return "account tables ok", nil
}

func existingForProvider(ex existingKeys, provider string) map[string]bool {
	m := map[string]bool{}
	for k := range ex.refresh {
		m["refresh:"+k] = true
	}
	for k := range ex.access {
		m["access:"+k] = true
	}
	return m
}

func firstNonEmptyString(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return strings.TrimSpace(x)
		}
	}
	return ""
}

func ensureDevDBPath(path string) error {
	// Production mode: skip dev DB restriction
	if strings.ToLower(strings.TrimSpace(os.Getenv("NRTUI_DEV_MODE"))) == "false" {
		return nil
	}
	clean := filepath.Clean(path)
	dev := filepath.Clean(filepath.Join(filepath.Dir(os.Args[0]), ".dev")) + string(os.PathSeparator)
	if strings.HasPrefix(clean, dev) {
		return nil
	}
	return fmt.Errorf("direct DB write blocked outside dev DB: %s (set dev_mode=false in 9rtui.ini to allow)", clean)
}

func sqliteFileDSN(path, query string) string {
	p := filepath.ToSlash(filepath.Clean(path))
	if runtime.GOOS == "windows" && len(p) >= 2 && p[1] == ':' {
		p = "/" + p
	}
	if strings.TrimSpace(query) == "" {
		return "file:" + p
	}
	return "file:" + p + "?" + query
}

func default9RouterDBPath() string {
	if runtime.GOOS == "windows" {
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			return filepath.Join(appData, "9router", "db", "data.sqlite")
		}
		if dir, err := os.UserConfigDir(); err == nil && strings.TrimSpace(dir) != "" {
			return filepath.Join(dir, "9router", "db", "data.sqlite")
		}
	}
	if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		return filepath.Join(home, ".9router", "db", "data.sqlite")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".9router", "db", "data.sqlite")
	}
	return filepath.Join(".", ".9router", "db", "data.sqlite")
}

func importLabel(provider string) string {
	switch provider {
	case "codex":
		return "OpenAI Codex"
	case "antigravity":
		return "Anti Gravity"
	case "kiro":
		return "Kiro"
	default:
		return provider
	}
}

func makeID(provider, seed string) string {
	h := tokenHash(provider + ":" + seed)
	if len(h) >= 32 {
		return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func Summary(res Result) string {
	parts := []string{fmt.Sprintf("selected=%d", res.Selected), fmt.Sprintf("available=%d", res.Available), fmt.Sprintf("skipped=%d", res.Skipped), fmt.Sprintf("ok=%d", res.OK), fmt.Sprintf("fail=%d", res.Fail)}
	if res.LogPath != "" {
		parts = append(parts, "log="+res.LogPath)
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}
