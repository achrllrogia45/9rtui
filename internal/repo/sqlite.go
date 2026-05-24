package repo

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/9rtui/9rtui/internal/domain"
	"github.com/9rtui/9rtui/internal/routerapi"
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

func (r *Repo) officialClient() (*routerapi.Client, error) {
	return routerapi.New(os.Getenv("NRTUI_API"))
}

func (r *Repo) officialExport(ctx context.Context) (routerapi.Backup, error) {
	c, err := r.officialClient()
	if err != nil {
		return nil, err
	}
	return c.ExportDatabase(ctx)
}

func (r *Repo) officialImport(ctx context.Context, b routerapi.Backup) error {
	c, err := r.officialClient()
	if err != nil {
		return err
	}
	return c.ImportDatabase(ctx, b)
}

func (r *Repo) ImportAccountBundle(path string) (int64, error) {
	incoming, err := bundleConnectionsFromFile(path)
	if err != nil {
		return 0, err
	}
	if len(incoming) == 0 {
		return 0, fmt.Errorf("import file has no providerConnections")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	backup, err := r.officialExport(ctx)
	if err != nil {
		return 0, err
	}
	conns := routerapi.ProviderConnections(backup)
	added := mergeMissingConnections(&conns, incoming)
	routerapi.SetProviderConnections(backup, conns)
	if added == 0 {
		return 0, nil
	}
	if err := r.officialImport(ctx, backup); err != nil {
		return added, err
	}
	return added, nil
}

func accountExportFromOfficial(c map[string]any) domain.AccountExport {
	pick := func(k string) string { s, _ := c[k].(string); return s }
	pri := 0
	if f, ok := c["priority"].(float64); ok {
		pri = int(f)
	}
	data := map[string]any{}
	for k, v := range c {
		switch k {
		case "id", "provider", "authType", "name", "email", "priority", "isActive", "createdAt", "updatedAt":
		default:
			data[k] = v
		}
	}
	return domain.AccountExport{ID: pick("id"), Provider: pick("provider"), AuthType: pick("authType"), Name: pick("name"), Email: pick("email"), Priority: pri, IsActive: routerapi.Bool(c["isActive"]), Data: data, CreatedAt: pick("createdAt"), UpdatedAt: pick("updatedAt")}
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

type APIKey struct {
	ID        string
	Key       string
	Name      string
	MachineID string
	IsActive  bool
	CreatedAt string
}

func (r *Repo) ListAPIKeys() ([]APIKey, error) {
	db, err := r.open()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, key, COALESCE(name,''), COALESCE(machineId,''), COALESCE(isActive,0), COALESCE(createdAt,'') FROM apiKeys ORDER BY createdAt DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		var active int
		if err := rows.Scan(&k.ID, &k.Key, &k.Name, &k.MachineID, &active, &k.CreatedAt); err != nil {
			return nil, err
		}
		k.IsActive = active != 0
		out = append(out, k)
	}
	return out, rows.Err()
}

func MaskKey(s string) string {
	if len(s) <= 10 {
		return s
	}
	return s[:6] + "..." + s[len(s)-4:]
}

func (r *Repo) CreateAPIKey(name, key string) (APIKey, error) {
	name = strings.TrimSpace(name)
	key = strings.TrimSpace(key)
	if key == "" {
		key = "sk-" + randomHex(24)
	}
	if len(key) < 8 || strings.ContainsAny(key, " \t\r\n") {
		return APIKey{}, fmt.Errorf("invalid api key: need >=8 chars and no whitespace")
	}
	db, err := r.open()
	if err != nil {
		return APIKey{}, err
	}
	defer db.Close()
	id := uuidLike()
	machine := machineIDFromRouterHome()
	created := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	_, err = db.Exec(`INSERT INTO apiKeys (id, key, name, machineId, isActive, createdAt) VALUES (?, ?, ?, ?, 1, ?)`, id, key, name, machine, created)
	if err != nil {
		return APIKey{}, err
	}
	return APIKey{ID: id, Key: key, Name: name, MachineID: machine, IsActive: true, CreatedAt: created}, nil
}

func (r *Repo) UpdateAPIKey(id, name, key string) error {
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	key = strings.TrimSpace(key)
	if id == "" {
		return fmt.Errorf("api key id required")
	}
	if key != "" && (len(key) < 8 || strings.ContainsAny(key, " \t\r\n")) {
		return fmt.Errorf("invalid api key: need >=8 chars and no whitespace")
	}
	db, err := r.open()
	if err != nil {
		return err
	}
	defer db.Close()
	if key == "" {
		_, err = db.Exec(`UPDATE apiKeys SET name=? WHERE id=?`, name, id)
	} else {
		_, err = db.Exec(`UPDATE apiKeys SET name=?, key=? WHERE id=?`, name, key, id)
	}
	return err
}

func (r *Repo) ToggleAPIKey(id string) error {
	db, err := r.open()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`UPDATE apiKeys SET isActive=CASE WHEN COALESCE(isActive,0)=0 THEN 1 ELSE 0 END WHERE id=?`, strings.TrimSpace(id))
	return err
}

func (r *Repo) DeleteAPIKey(id string) error {
	db, err := r.open()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`DELETE FROM apiKeys WHERE id=?`, strings.TrimSpace(id))
	return err
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func uuidLike() string {
	s := randomHex(16)
	if len(s) < 32 {
		return s
	}
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

func machineIDFromRouterHome() string {
	base := os.Getenv("DATA_DIR")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".9router")
	}
	b, _ := os.ReadFile(filepath.Join(base, "machine-id"))
	return strings.TrimSpace(string(b))
}

type BackupSlot string

const (
	BackupSnap   BackupSlot = "snap"
	BackupAuto   BackupSlot = "auto"
	BackupVacIdx BackupSlot = "vacidx"
)

type BackupCopyMethod string

const (
	BackupCopyNone          BackupCopyMethod = "none"
	BackupCopyVacuum        BackupCopyMethod = "vacuum"
	BackupCopyVacuumCleanup BackupCopyMethod = "vacuum_cleanup"
)

func ParseBackupCopyMethod(s string) (BackupCopyMethod, error) {
	switch BackupCopyMethod(strings.TrimSpace(s)) {
	case "", BackupCopyNone:
		return BackupCopyNone, nil
	case BackupCopyVacuum:
		return BackupCopyVacuum, nil
	case BackupCopyVacuumCleanup:
		return BackupCopyVacuumCleanup, nil
	default:
		return "", fmt.Errorf("invalid backup method %q (want none|vacuum|vacuum_cleanup)", s)
	}
}

func (r *Repo) LogsDir() string   { return r.logDir }
func (r *Repo) backupDir() string { return filepath.Join(r.LogsDir(), "full-backups") }
func (r *Repo) bak1() string      { return filepath.Join(r.backupDir(), filepath.Base(r.Path)) }
func (r *Repo) bak2() string      { return filepath.Join(r.backupDir(), filepath.Base(r.Path)) }

func (r *Repo) routerBackupDir() string { return filepath.Join(filepath.Dir(r.Path), "backup") }
func (r *Repo) backupSlotPath(slot BackupSlot) string {
	return filepath.Join(r.routerBackupDir(), filepath.Base(r.Path)+".bak-"+string(slot))
}
func (r *Repo) backupSidecarPath(slot BackupSlot) string { return r.backupSlotPath(slot) + ".txt" }

func (r *Repo) BackupSlot(slot BackupSlot, method BackupCopyMethod) (string, error) {
	if method == "" {
		method = BackupCopyNone
	}
	path := r.backupSlotPath(slot)
	before := fileSize(r.Path)
	if err := sqliteBackupWithMethod(r.Path, path, method); err != nil {
		return "", err
	}
	after := fileSize(path)
	if err := r.writeBackupSidecar(slot, method, before, after); err != nil {
		return "", err
	}
	return path, nil
}

func (r *Repo) Snapshot(method BackupCopyMethod) (string, error) {
	return r.BackupSlot(BackupSnap, method)
}
func (r *Repo) EnsureDailyBackup() (string, error) {
	return r.EnsureDailyBackupWithMethod(BackupCopyNone)
}
func (r *Repo) EnsureDailyBackupWithMethod(method BackupCopyMethod) (string, error) {
	if method == "" {
		method = BackupCopyNone
	}
	path := r.backupSlotPath(BackupAuto)
	metaPath := r.backupSidecarPath(BackupAuto)
	if st, err := os.Stat(metaPath); err == nil && time.Now().Format("20060102") == st.ModTime().Format("20060102") {
		if b, err := os.ReadFile(metaPath); err == nil && strings.Contains(string(b), "method="+string(method)+"\n") {
			return path, nil
		}
	}
	return r.BackupSlot(BackupAuto, method)
}
func (r *Repo) ForceTimestampBackup() (string, error) {
	return r.BackupSlot(BackupVacIdx, BackupCopyVacuum)
}

func (r *Repo) writeBackupSidecar(slot BackupSlot, method BackupCopyMethod, before, after int64) error {
	body := fmt.Sprintf("created_at=%s\nmethod=%s\noperation=%s\nsource=%s\nbackup=%s\nbefore_size=%s\nafter_size=%s\n", time.Now().Format("20060102-1504"), method, slot, r.Path, r.backupSlotPath(slot), humanBytes(before), humanBytes(after))
	return os.WriteFile(r.backupSidecarPath(slot), []byte(body), 0600)
}

func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

func Kill9Router() string {
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/f", "/im", "node.exe").Run()
		return "taskkill /f /im node.exe"
	}
	_ = exec.Command("pkill", "-9", "9router").Run()
	return "pkill -9 9router"
}

func (r *Repo) Reindex() (string, error) { return r.ReindexWithBackup(BackupCopyNone) }
func (r *Repo) ReindexWithBackup(method BackupCopyMethod) (string, error) {
	kill := Kill9Router()
	db, err := r.open()
	if err != nil {
		return "", err
	}
	defer db.Close()
	if _, err := db.Exec(`REINDEX`); err != nil {
		return "", err
	}
	if err := accountCheck(db); err != nil {
		return "", err
	}
	backup, err := r.BackupSlot(BackupVacIdx, method)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s; REINDEX done; backup: %s (%s)", kill, backup, method), nil
}

func (r *Repo) Vacuum() (string, error) { return r.VacuumWithBackup(BackupCopyNone) }
func (r *Repo) VacuumWithBackup(method BackupCopyMethod) (string, error) {
	kill := Kill9Router()
	db, err := r.open()
	if err != nil {
		return "", err
	}
	defer db.Close()
	before := fileSize(r.Path)
	if _, err := db.Exec(`VACUUM`); err != nil {
		return "", err
	}
	after := fileSize(r.Path)
	backup, err := r.BackupSlot(BackupVacIdx, method)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s; vacuum: %s -> %s; backup: %s (%s)", kill, humanBytes(before), humanBytes(after), backup, method), nil
}

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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	backup, err := r.officialExport(ctx)
	if err != nil {
		return "", err
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	conns := routerapi.ProviderConnections(backup)
	kept := make([]map[string]any, 0, len(conns))
	var xs []domain.AccountExport
	for _, c := range conns {
		id := routerapi.AccountID(c)
		if len(want) > 0 && !want[id] {
			continue
		}
		kept = append(kept, c)
		xs = append(xs, accountExportFromOfficial(c))
	}
	bundle := map[string]any{
		"format":              "9router-providerConnections-v1",
		"createdAt":           time.Now().UTC().Format(time.RFC3339),
		"providerConnections": kept,
	}
	b, err := json.MarshalIndent(bundle, "", "  ")
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

func accountBundle(conns []map[string]any, reason string, now time.Time) map[string]any {
	return map[string]any{
		"providerConnections": conns,
	}
}

func writeAccountBundle(path string, conns []map[string]any, reason string) error {
	bb, err := json.MarshalIndent(accountBundle(conns, reason, time.Now()), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(bb, '\n'), 0600)
}

func providerCountFromConnections(conns []map[string]any) map[string]int {
	out := map[string]int{}
	for _, c := range conns {
		if p, _ := c["provider"].(string); p != "" {
			out[p]++
		}
	}
	return out
}

func bundleConnectionsFromFile(path string) ([]map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, err
	}
	return routerapi.ProviderConnections(routerapi.Backup(root)), nil
}

func refreshTokenOf(c map[string]any) string {
	if s, _ := c["refreshToken"].(string); strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	if data, ok := c["data"].(map[string]any); ok {
		if s, _ := data["refreshToken"].(string); strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	if s, _ := c["data"].(string); strings.TrimSpace(s) != "" {
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) == nil {
			if rt, _ := m["refreshToken"].(string); strings.TrimSpace(rt) != "" {
				return strings.TrimSpace(rt)
			}
		}
	}
	return ""
}

func connectionID(c map[string]any) string {
	if s, _ := c["id"].(string); strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	return ""
}

func mergeMissingConnections(conns *[]map[string]any, incoming []map[string]any) int64 {
	seenRefresh := map[string]bool{}
	seenID := map[string]bool{}
	for _, c := range *conns {
		if rt := refreshTokenOf(c); rt != "" {
			seenRefresh[rt] = true
		}
		if id := connectionID(c); id != "" {
			seenID[id] = true
		}
	}
	var added int64
	now := time.Now().UTC().Format(time.RFC3339)
	for _, c := range incoming {
		if rt := refreshTokenOf(c); rt != "" {
			if seenRefresh[rt] {
				continue
			}
			seenRefresh[rt] = true
		} else if id := connectionID(c); id != "" {
			if seenID[id] {
				continue
			}
			seenID[id] = true
		} else {
			continue
		}
		if _, ok := c["createdAt"]; !ok {
			c["createdAt"] = now
		}
		c["updatedAt"] = now
		*conns = append(*conns, c)
		added++
	}
	return added
}

func (r *Repo) writeDeleteLog(conns []map[string]any) (string, error) {
	if len(conns) == 0 {
		return "", nil
	}
	if err := os.MkdirAll(r.LogsDir(), 0755); err != nil {
		return "", err
	}
	xs := make([]domain.AccountExport, 0, len(conns))
	for _, c := range conns {
		xs = append(xs, accountExportFromOfficial(c))
	}
	path := filepath.Join(r.LogsDir(), deleteBundleFileName(xs, time.Now()))
	return path, writeAccountBundle(path, conns, "delete")
}

func (r *Repo) SetAccountsActive(ids []string, active bool) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	backup, err := r.officialExport(ctx)
	if err != nil {
		return 0, err
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	conns := routerapi.ProviderConnections(backup)
	now := time.Now().UTC().Format(time.RFC3339)
	var total int64
	for _, c := range conns {
		if want[routerapi.AccountID(c)] {
			c["isActive"] = active
			c["updatedAt"] = now
			total++
		}
	}
	routerapi.SetProviderConnections(backup, conns)
	if err := r.officialImport(ctx, backup); err != nil {
		return total, err
	}
	return total, nil
}

func (r *Repo) DeleteAccounts(ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	backup, err := r.officialExport(ctx)
	if err != nil {
		return 0, err
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	conns := routerapi.ProviderConnections(backup)
	kept := make([]map[string]any, 0, len(conns))
	deletedConns := make([]map[string]any, 0, len(ids))
	var deleted int64
	for _, c := range conns {
		if want[routerapi.AccountID(c)] {
			deleted++
			deletedConns = append(deletedConns, c)
			continue
		}
		kept = append(kept, c)
	}
	if _, err := r.writeDeleteLog(deletedConns); err != nil {
		return deleted, err
	}
	routerapi.SetProviderConnections(backup, kept)
	if err := r.officialImport(ctx, backup); err != nil {
		return deleted, err
	}
	return deleted, nil
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
		var root map[string]any
		if err := json.Unmarshal(b, &root); err != nil {
			continue
		}
		conns := routerapi.ProviderConnections(routerapi.Backup(root))
		if len(conns) == 0 {
			var log UndoLog
			if err := json.Unmarshal(b, &log); err != nil || len(log.Rows) == 0 {
				continue
			}
			conns = make([]map[string]any, 0, len(log.Rows))
			for _, row := range log.Rows {
				m := map[string]any{"id": row.ID, "provider": row.Provider, "authType": row.AuthType, "name": row.Name, "email": row.Email, "priority": row.Priority, "isActive": row.IsActive != 0, "createdAt": row.CreatedAt, "updatedAt": row.UpdatedAt}
				if strings.TrimSpace(row.Data) != "" {
					var data map[string]any
					if json.Unmarshal([]byte(row.Data), &data) == nil {
						for k, v := range data {
							m[k] = v
						}
					} else {
						m["data"] = row.Data
					}
				}
				conns = append(conns, m)
			}
		}
		info := UndoInfo{Path: p, Modified: st.ModTime().Format(time.RFC3339), Size: st.Size(), Accounts: len(conns), ProviderCount: providerCountFromConnections(conns)}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified > out[j].Modified })
	return out, nil
}

func (r *Repo) RestoreUndo(path string) (int64, error) {
	if filepath.Dir(path) != r.LogsDir() {
		return 0, fmt.Errorf("restore log must be under .tui-logs")
	}
	incoming, err := bundleConnectionsFromFile(path)
	if err != nil {
		return 0, err
	}
	if len(incoming) == 0 {
		return 0, fmt.Errorf("restore log has no providerConnections")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	backup, err := r.officialExport(ctx)
	if err != nil {
		return 0, err
	}
	conns := routerapi.ProviderConnections(backup)
	restored := mergeMissingConnections(&conns, incoming)
	routerapi.SetProviderConnections(backup, conns)
	if err := r.officialImport(ctx, backup); err != nil {
		return restored, err
	}
	return restored, nil
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

func deleteBundleFileName(xs []domain.AccountExport, now time.Time) string {
	suffix := timestampSuffix(now)
	if len(xs) == 1 {
		x := xs[0]
		base := firstNonEmpty(x.Email, x.Name, x.ID)
		return safeName(base) + "-delete-" + suffix + ".json"
	}
	return fmt.Sprintf("delete-%d-accounts-%s.json", len(xs), suffix)
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

func sqliteBackup(src, dst string) error { return sqliteBackupWithMethod(src, dst, BackupCopyVacuum) }

func sqliteBackupWithMethod(src, dst string, method BackupCopyMethod) error {
	switch method {
	case "", BackupCopyNone:
		return sqliteBackupCopy(src, dst)
	case BackupCopyVacuum:
		return sqliteBackupVacuumInto(src, dst)
	case BackupCopyVacuumCleanup:
		return sqliteBackupVacuumCleanup(src, dst)
	default:
		return fmt.Errorf("unknown backup method: %s", method)
	}
}

func sqliteBackupCopy(src, dst string) error {
	db, err := sql.Open("sqlite", sqliteFileDSN(src, "_busy_timeout=10000"))
	if err != nil {
		return err
	}
	_, ckErr := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	closeErr := db.Close()
	if ckErr != nil {
		return ckErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	return verifySQLite(dst)
}

func sqliteBackupVacuumInto(src, dst string) error {
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

func sqliteBackupVacuumCleanup(src, dst string) error {
	if err := sqliteBackupVacuumInto(src, dst); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", sqliteFileDSN(dst, "_busy_timeout=10000"))
	if err != nil {
		return err
	}
	defer db.Close()
	for _, table := range []string{"requestDetails", "usageHistory", "usageDaily"} {
		if err := deleteIfTableExists(db, table); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`VACUUM`); err != nil {
		return err
	}
	return verifySQLite(dst)
}

func deleteIfTableExists(db *sql.DB, table string) error {
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = db.Exec(`DELETE FROM ` + table)
	return err
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
