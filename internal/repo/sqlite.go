package repo

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/9rtui/9rtui/internal/domain"
	_ "modernc.org/sqlite"
)

type Repo struct {
	Path   string
	logDir string
}

func New(path string) *Repo { return &Repo{Path: path, logDir: defaultLogDir()} }
func (r *Repo) open() (*sql.DB, error) {
	db, err := sql.Open("sqlite", sqliteFileDSN(r.Path, "_busy_timeout=10000&_journal_mode=WAL&_foreign_keys=on"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
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

func defaultLogDir() string {
	if v := os.Getenv("NRTUI_LOG_DIR"); v != "" {
		return v
	}
	return "./.tui-logs"
}

func defaultAccountsDir() string {
	if v := os.Getenv("NRTUI_ACCOUNTS_PATH"); v != "" {
		return strings.TrimRight(v, string(os.PathSeparator))
	}
	return "./.accounts"
}

type accountRow struct {
	ID        string `json:"id"`
	Provider  string `json:"provider"`
	AuthType  string `json:"authType"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Priority  int    `json:"priority"`
	IsActive  int    `json:"isActive"`
	Data      string `json:"data"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}
type UndoLog struct {
	Op        string       `json:"op"`
	CreatedAt string       `json:"createdAt"`
	Rows      []accountRow `json:"rows"`
}

type UndoInfo struct {
	Path          string
	Modified      string
	Size          int64
	Accounts      int
	ProviderCount map[string]int
}

func (r *Repo) LogsDir() string   { return r.logDir }
func (r *Repo) backupDir() string { return filepath.Join(r.LogsDir(), "full-backups") }
func (r *Repo) bak1() string      { return filepath.Join(r.backupDir(), filepath.Base(r.Path)) }
func (r *Repo) bak2() string      { return filepath.Join(r.backupDir(), filepath.Base(r.Path)) }

func (r *Repo) ListAccounts() ([]domain.Account, error) {
	db, err := r.open()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, provider, authType, COALESCE(name,''), COALESCE(email,''), COALESCE(priority,0), COALESCE(isActive,0), createdAt, updatedAt FROM providerConnections NOT INDEXED ORDER BY provider, priority, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Account
	for rows.Next() {
		var a domain.Account
		var active int
		if err := rows.Scan(&a.ID, &a.Provider, &a.AuthType, &a.Name, &a.Email, &a.Priority, &active, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		a.IsActive = active != 0
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repo) ListProviderIDs() ([]string, error) {
	db, err := r.open()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id FROM providerNodes ORDER BY type, name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (r *Repo) GetAccount(id string) (domain.AccountExport, error) {
	db, err := r.open()
	if err != nil {
		return domain.AccountExport{}, err
	}
	defer db.Close()
	var x accountRow
	if err := db.QueryRow(`SELECT id, provider, authType, COALESCE(name,''), COALESCE(email,''), COALESCE(priority,0), COALESCE(isActive,0), data, createdAt, updatedAt FROM providerConnections NOT INDEXED WHERE id = ?`, id).Scan(&x.ID, &x.Provider, &x.AuthType, &x.Name, &x.Email, &x.Priority, &x.IsActive, &x.Data, &x.CreatedAt, &x.UpdatedAt); err != nil {
		return domain.AccountExport{}, err
	}
	var data any
	if strings.TrimSpace(x.Data) != "" {
		if err := json.Unmarshal([]byte(x.Data), &data); err != nil {
			data = x.Data
		}
	}
	return domain.AccountExport{ID: x.ID, Provider: x.Provider, AuthType: x.AuthType, Name: x.Name, Email: x.Email, Priority: x.Priority, IsActive: x.IsActive != 0, Data: data, CreatedAt: x.CreatedAt, UpdatedAt: x.UpdatedAt}, nil
}

func (r *Repo) GetAccounts(ids []string) ([]domain.AccountExport, error) {
	out := make([]domain.AccountExport, 0, len(ids))
	for _, id := range ids {
		x, err := r.GetAccount(id)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, nil
}

func (r *Repo) ExportAccounts(ids []string) (string, error) {
	xs, err := r.GetAccounts(ids)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"warning":   "contains full 9Router account credentials/secrets",
		"createdAt": time.Now().Format(time.RFC3339),
		"count":     len(xs),
		"accounts":  xs,
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	exportDir := defaultAccountsDir()
	if err := os.MkdirAll(exportDir, 0755); err != nil {
		return "", err
	}
	allSelected := false
	if all, err := r.ListAccounts(); err == nil && len(xs) == len(all) && len(xs) > 1 {
		allSelected = true
	}
	name := exportFileName(xs, allSelected, time.Now())
	path := filepath.Join(exportDir, name)
	if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
		return "", err
	}
	return path, nil
}

func (r *Repo) ExportAccount(id string) (string, error) {
	return r.ExportAccounts([]string{id})
}

func (r *Repo) readRows(tx *sql.Tx, ids []string) ([]accountRow, error) {
	stmt, err := tx.Prepare(`SELECT id, provider, authType, COALESCE(name,''), COALESCE(email,''), COALESCE(priority,0), COALESCE(isActive,0), data, createdAt, updatedAt FROM providerConnections NOT INDEXED WHERE id = ?`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	var rows []accountRow
	for _, id := range ids {
		var x accountRow
		if err := stmt.QueryRow(id).Scan(&x.ID, &x.Provider, &x.AuthType, &x.Name, &x.Email, &x.Priority, &x.IsActive, &x.Data, &x.CreatedAt, &x.UpdatedAt); err == sql.ErrNoRows {
			continue
		} else if err != nil {
			return nil, err
		}
		rows = append(rows, x)
	}
	return rows, nil
}

func (r *Repo) writeUndoLog(rows []accountRow) (string, error) {
	if len(rows) == 0 {
		return "", nil
	}
	if err := os.MkdirAll(r.LogsDir(), 0755); err != nil {
		return "", err
	}
	log := UndoLog{Op: "delete-providerConnections", CreatedAt: time.Now().Format(time.RFC3339), Rows: rows}
	b, err := json.MarshalIndent(log, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(r.LogsDir(), deleteLogFileName(rows, time.Now()))
	return path, os.WriteFile(path, b, 0600)
}

func (r *Repo) SetAccountsActive(ids []string, active bool) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	if err := ensureDevDBPath(r.Path); err != nil {
		return 0, err
	}
	db, err := r.open()
	if err != nil {
		return 0, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	val := 0
	if active {
		val = 1
	}
	stmt, err := tx.Prepare(`UPDATE providerConnections SET isActive = ?, updatedAt = ? WHERE id = ?`)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	defer stmt.Close()
	now := time.Now().UTC().Format(time.RFC3339)
	var total int64
	for _, id := range ids {
		res, err := stmt.Exec(val, now, id)
		if err != nil {
			tx.Rollback()
			return 0, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if err := accountCheck(db); err != nil {
		return total, err
	}
	return total, nil
}

func (r *Repo) DeleteAccounts(ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	if err := ensureDevDBPath(r.Path); err != nil {
		return 0, err
	}
	db, err := r.open()
	if err != nil {
		return 0, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	rows, err := r.readRows(tx, ids)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	if _, err := r.writeUndoLog(rows); err != nil {
		tx.Rollback()
		return 0, err
	}
	stmt, err := tx.Prepare(`DELETE FROM providerConnections WHERE id = ?`)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	defer stmt.Close()
	var total int64
	for _, id := range ids {
		res, err := stmt.Exec(id)
		if err != nil {
			tx.Rollback()
			return 0, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if err := accountCheck(db); err != nil {
		return total, err
	}
	return total, nil
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

func quickCheck(db *sql.DB) error {
	var status string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&status); err != nil {
		return err
	}
	if status != "ok" {
		return fmt.Errorf("sqlite quick_check failed: %s", status)
	}
	return nil
}

func accountCheck(db *sql.DB) error {
	// 9Router can have corruption in huge telemetry tables (requestDetails/usageHistory)
	// while account tables remain readable. Account actions should verify account-critical
	// tables instead of failing writes because unrelated log/history btrees are damaged.
	for _, q := range []string{
		`SELECT COUNT(*) FROM providerConnections`,
		`SELECT COUNT(*) FROM providerNodes`,
	} {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) UndoLogs() ([]UndoInfo, error) {
	entries, err := os.ReadDir(r.LogsDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []UndoInfo
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		p := filepath.Join(r.LogsDir(), e.Name())
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var log UndoLog
		if err := json.Unmarshal(b, &log); err != nil || len(log.Rows) == 0 {
			var rows []accountRow
			if err2 := json.Unmarshal(b, &rows); err2 != nil || len(rows) == 0 {
				continue
			}
			log.Rows = rows
		}
		info := UndoInfo{Path: p, Modified: st.ModTime().Format(time.RFC3339), Size: st.Size(), Accounts: len(log.Rows), ProviderCount: map[string]int{}}
		for _, row := range log.Rows {
			info.ProviderCount[row.Provider]++
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified > out[j].Modified })
	return out, nil
}

func (r *Repo) RestoreUndo(path string) (int64, error) {
	if filepath.Dir(path) != r.LogsDir() {
		return 0, fmt.Errorf("restore log must be under .tui-logs")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var log UndoLog
	if err := json.Unmarshal(b, &log); err != nil {
		return 0, err
	}
	db, err := r.open()
	if err != nil {
		return 0, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO providerConnections (id,provider,authType,name,email,priority,isActive,data,createdAt,updatedAt) VALUES (?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	defer stmt.Close()
	var n int64
	for _, x := range log.Rows {
		res, err := stmt.Exec(x.ID, x.Provider, x.AuthType, x.Name, x.Email, x.Priority, x.IsActive, x.Data, x.CreatedAt, x.UpdatedAt)
		if err != nil {
			tx.Rollback()
			return 0, err
		}
		a, _ := res.RowsAffected()
		n += a
	}
	return n, tx.Commit()
}

func (r *Repo) CleanVacuum() (string, error) {
	db, err := r.open()
	if err != nil {
		return "", err
	}
	defer db.Close()
	var before int64
	if st, err := os.Stat(r.Path); err == nil {
		before = st.Size()
	}
	if err := r.BackupRotate(); err != nil {
		return "", err
	}
	backup := r.bak1()
	for _, q := range []string{`DELETE FROM requestDetails`, `DELETE FROM usageHistory`, `DELETE FROM usageDaily`} {
		if _, err := db.Exec(q); err != nil {
			return "", err
		}
	}
	if _, err := db.Exec(`VACUUM`); err != nil {
		return "", err
	}
	var after int64
	if st, err := os.Stat(r.Path); err == nil {
		after = st.Size()
	}
	return fmt.Sprintf("backup: %s; cleaned logs, vacuumed: %s -> %s", backup, humanBytes(before), humanBytes(after)), nil
}

func humanBytes(n int64) string {
	if n > 1024*1024*1024 {
		return fmt.Sprintf("%.1fG", float64(n)/(1024*1024*1024))
	}
	if n > 1024*1024 {
		return fmt.Sprintf("%.1fM", float64(n)/(1024*1024))
	}
	if n > 1024 {
		return fmt.Sprintf("%.1fK", float64(n)/1024)
	}
	return fmt.Sprintf("%dB", n)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	cerr := out.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if cerr != nil {
		os.Remove(tmp)
		return cerr
	}
	return os.Rename(tmp, dst)
}
func (r *Repo) BackupRotate() error {
	bak := r.bak1()
	if err := os.MkdirAll(r.backupDir(), 0755); err != nil {
		return err
	}
	return sqliteBackup(r.Path, bak)
}
func (r *Repo) BackupTo(path string) error {
	if path != r.bak1() {
		return fmt.Errorf("backup path must be full-backups/%s", filepath.Base(r.Path))
	}
	return sqliteBackup(r.Path, path)
}
func (r *Repo) Restore(path string) error {
	bak := r.bak1()
	if path != bak {
		return fmt.Errorf("restore path must be full-backups/%s", filepath.Base(r.Path))
	}
	if _, err := os.Stat(path); err != nil {
		return err
	}
	return copyFile(path, r.Path)
}
func (r *Repo) BackupInfos() ([]domain.BackupInfo, error) {
	paths := []string{r.Path, r.bak1()}
	var infos []domain.BackupInfo
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		info := domain.BackupInfo{Path: p, Modified: st.ModTime().Format(time.RFC3339), Size: st.Size(), ProviderCount: map[string]int{}}
		accs, err := New(p).ListAccounts()
		if err == nil {
			info.Accounts = len(accs)
			for _, a := range accs {
				info.ProviderCount[a.Provider]++
			}
		}
		infos = append(infos, info)
	}
	return infos, nil
}

var CoreProviders = []string{
	"all",
	"claude",
	"codex",
	"gemini-cli",
	"github",
	"antigravity",
	"iflow",
	"qwen",
	"kiro",
	"openrouter",
	"glm",
	"minimax",
	"kimi",
	"openai",
	"anthropic",
	"gemini",
}

// MediaProviders is intentionally empty until 9Router exposes real media provider
// definitions in source/DB. Do not invent provider IDs here.
var MediaProviders = []string{}

func Providers(accounts []domain.Account) []string {
	return ProvidersWithKnown(accounts, nil)
}

func ProvidersWithKnown(accounts []domain.Account, known []string) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, p := range CoreProviders {
		add(p)
	}
	var custom []string
	for _, p := range known {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] || isKnownMediaProvider(p) {
			continue
		}
		custom = append(custom, p)
	}
	for _, a := range accounts {
		p := strings.TrimSpace(a.Provider)
		if p == "" || seen[p] || isKnownMediaProvider(p) {
			continue
		}
		custom = append(custom, p)
	}
	sort.Strings(custom)
	for _, p := range custom {
		add(p)
	}
	for _, p := range MediaProviders {
		add(p)
	}
	for _, p := range known {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] || !isKnownMediaProvider(p) {
			continue
		}
		add(p)
	}
	for _, a := range accounts {
		p := strings.TrimSpace(a.Provider)
		if p == "" || seen[p] || !isKnownMediaProvider(p) {
			continue
		}
		add(p)
	}
	return out
}

func isKnownMediaProvider(p string) bool {
	p = strings.ToLower(strings.TrimSpace(p))
	for _, x := range MediaProviders {
		if p == x {
			return true
		}
	}
	return false
}
func exportFileName(xs []domain.AccountExport, allSelected bool, now time.Time) string {
	suffix := timestampSuffix(now)
	if len(xs) == 1 {
		a := xs[0]
		base := firstNonEmpty(a.Email, a.Name, a.ID)
		return safeName(base) + "-" + suffix + ".json"
	}
	if allSelected {
		return "all-accounts-" + suffix + ".json"
	}
	providers := uniqueExportProviders(xs)
	if len(providers) == 1 {
		return fmt.Sprintf("%s-%d-accounts-%s.json", providerAbbrev(providers[0]), len(xs), suffix)
	}
	parts := make([]string, 0, len(providers))
	for _, p := range providers {
		parts = append(parts, providerAbbrev(p))
	}
	return fmt.Sprintf("%s-%d-accounts-%s.json", safeName(strings.Join(parts, "-")), len(xs), suffix)
}

func uniqueExportProviders(xs []domain.AccountExport) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		p := strings.TrimSpace(x.Provider)
		if p == "" {
			p = "unknown"
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func providerAbbrev(provider string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	switch p {
	case "claude":
		return "cc"
	case "codex", "openai-codex":
		return "cdx"
	case "gemini-cli":
		return "gmncli"
	case "github":
		return "gh"
	case "antigravity", "anti-gravity":
		return "ag"
	case "iflow":
		return "if"
	case "qwen":
		return "qw"
	case "kiro":
		return "kiro"
	case "openrouter":
		return "opnrtr"
	case "glm":
		return "glm"
	case "minimax":
		return "minimax"
	case "kimi":
		return "kimi"
	case "openai":
		return "openai"
	case "anthropic":
		return "anth"
	case "gemini":
		return "gmn"
	}
	if strings.HasPrefix(p, "openai-compatible") {
		return "oai-compat"
	}
	return safeName(p)
}

func deleteLogFileName(rows []accountRow, now time.Time) string {
	suffix := timestampSuffix(now)
	if len(rows) == 1 {
		x := rows[0]
		base := firstNonEmpty(x.Email, x.Name, x.ID)
		return safeName(base) + "-delete-" + suffix + ".json"
	}
	return fmt.Sprintf("delete-%d-accounts-%s.json", len(rows), suffix)
}

func timestampSuffix(t time.Time) string { return t.Format("20060102-1504") }

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return strings.TrimSpace(x)
		}
	}
	return "unknown"
}

func sqliteBackup(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	_ = os.Remove(tmp)
	db, err := sql.Open("sqlite", sqliteFileDSN(src, "_busy_timeout=10000&mode=ro"))
	if err != nil {
		return err
	}
	defer db.Close()
	quoted := strings.ReplaceAll(tmp, "'", "''")
	if _, err := db.Exec("VACUUM INTO '" + quoted + "'"); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := verifySQLite(tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func verifySQLite(path string) error {
	db, err := sql.Open("sqlite", sqliteFileDSN(path, "mode=ro"))
	if err != nil {
		return err
	}
	defer db.Close()
	var status string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&status); err != nil {
		return err
	}
	if status != "ok" {
		return fmt.Errorf("sqlite integrity_check failed: %s", status)
	}
	return nil
}

func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}
