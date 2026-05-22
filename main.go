package main

import (
	"bufio"
	"context"
	"database/sql"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/9rtui/9rtui/internal/importer"
	"github.com/9rtui/9rtui/internal/repo"
	"github.com/9rtui/9rtui/internal/tui"
	"github.com/9rtui/9rtui/internal/web"
	tea "github.com/charmbracelet/bubbletea"
	_ "modernc.org/sqlite"
)

var appDir = detectAppDir()

var version = "dev"
var commit = "unknown"
var buildDate = "unknown"

func main() {
	port := flag.Int("port", 20129, "web server port")
	showVersion := flag.Bool("version", false, "print version and exit")
	showVersionShort := flag.Bool("v", false, "print version and exit")
	flag.Usage = usage
	flag.Parse()
	args := flag.Args()
	if err := migrateLocalDirs(appDir); err != nil {
		fmt.Fprintln(os.Stderr, "migration warning:", err)
	}
	if *showVersion || *showVersionShort {
		fmt.Printf("9rtui %s (commit %s, built %s)\n", version, commit, buildDate)
		return
	}

	cfg, db, err := setupRuntime()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if len(args) > 0 {
		switch args[0] {
		case "version":
			fmt.Printf("9rtui %s (commit %s, built %s)\n", version, commit, buildDate)
			return
		case "config":
			printConfig(cfg, db)
			return
		case "web":
			handleWeb(args[1:], *port, db)
			return
		case "tui":
			runTUI(db)
			return
		case "stop":
			if err := web.StopHard(appDir); err != nil {
				fmt.Fprintln(os.Stderr, "stop failed:", err)
				os.Exit(1)
			}
			fmt.Println("9rtui stopped")
			return
		case "check-db":
			if err := checkDB(db); err != nil {
				fmt.Fprintln(os.Stderr, "DB FAILED:", err)
				os.Exit(1)
			}
			fmt.Println("DB OK:", db)
			return
		case "import-file":
			handleImportFile(args[1:], db)
			return
		case "export":
			handleExport(args[1:], db)
			return
		case "restart":
			_ = web.StopHard(appDir)
			handleWeb([]string{"start"}, *port, db)
			return
		}
	}
	_ = cfg
	runTUI(db)
}

func usage() {
	out := flag.CommandLine.Output()
	fmt.Fprintln(out, "9rtui - Terminal UI for 9Router accounts")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  9rtui                         open TUI")
	fmt.Fprintln(out, "  9rtui version                 print version and exit")
	fmt.Fprintln(out, "  9rtui config                  print resolved config")
	fmt.Fprintln(out, "  9rtui tui                     open TUI")
	fmt.Fprintln(out, "  9rtui web start               run web TUI at localhost:20129")
	fmt.Fprintln(out, "  9rtui web expose              run web TUI at 0.0.0.0:20129")
	fmt.Fprintln(out, "  9rtui stop                    hard-stop all running 9rtui web servers")
	fmt.Fprintln(out, "  9rtui check-db                verify configured 9Router DB")
	fmt.Fprintln(out, "  9rtui import-file -provider kiro -file accounts.json")
	fmt.Fprintln(out, "  9rtui export [-provider kiro] export account JSON to .accounts")
	fmt.Fprintln(out, "  9rtui web stop                alias for stop")
	fmt.Fprintln(out, "  9rtui web restart             restart web server")
	fmt.Fprintln(out, "  9rtui restart                 restart web server")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Options:")
	flag.PrintDefaults()
}

func printConfig(cfg map[string]string, db string) {
	fmt.Println("project_dir:", appDir)
	fmt.Println("db_path:", db)
	fmt.Println("log_dir:", resolveAppPath(expandConfigPath(cfg["log_dir"])))
	fmt.Println("accounts_path:", resolveAppPath(expandConfigPath(cfg["accounts_path"])))
	fmt.Println("api_base:", cfg["api_base"])
	fmt.Println("dev_mode:", cfg["dev_mode"])
	fmt.Println("version:", version)
	fmt.Println("commit:", commit)
	fmt.Println("built:", buildDate)
}

func handleImportFile(args []string, dbPath string) {
	fs := flag.NewFlagSet("import-file", flag.ExitOnError)
	provider := fs.String("provider", "kiro", "provider name")
	file := fs.String("file", "", "account JSON file")
	dryRun := fs.Bool("dry-run", false, "preview without writing")
	limit := fs.Int("limit", 0, "max rows to import")
	_ = fs.Parse(args)
	if strings.TrimSpace(*file) == "" {
		fmt.Fprintln(os.Stderr, "import-file requires -file")
		os.Exit(2)
	}
	res, err := importer.RunProvider(context.Background(), importer.ImportOptions{AccountsPath: *file, DBPath: dbPath, DoImport: !*dryRun, DryRun: *dryRun, IncludeInactive: true, Limit: *limit}, *provider)
	if err != nil {
		fmt.Fprintln(os.Stderr, "import failed:", err)
		os.Exit(1)
	}
	fmt.Printf("selected=%d ok=%d fail=%d skipped=%d db=%s log=%s\n", res.Selected, res.OK, res.Fail, res.Skipped, res.DBCheck, res.LogPath)
}

func handleExport(args []string, dbPath string) {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	provider := fs.String("provider", "", "provider filter")
	_ = fs.Parse(args)
	r := repo.New(dbPath)
	rows, err := r.ListAccounts()
	if err != nil {
		fmt.Fprintln(os.Stderr, "export failed:", err)
		os.Exit(1)
	}
	ids := make([]string, 0, len(rows))
	for _, a := range rows {
		if *provider == "" || strings.EqualFold(a.Provider, *provider) {
			ids = append(ids, a.ID)
		}
	}
	if len(ids) == 0 {
		fmt.Fprintln(os.Stderr, "export failed: no accounts matched")
		os.Exit(1)
	}
	path, err := r.ExportAccounts(ids)
	if err != nil {
		fmt.Fprintln(os.Stderr, "export failed:", err)
		os.Exit(1)
	}
	fmt.Println("exported", path)
}

func setupRuntime() (map[string]string, string, error) {
	cfgPath := filepath.Join(appDir, "9rtui.ini")
	cfg := loadINI(cfgPath)

	dbEnv := strings.TrimSpace(os.Getenv("NRTUI_DB"))
	db := resolveAppPath(expandConfigPath(firstNonEmpty(dbEnv, cfg["db_path"])))
	if db == "" || (dbEnv == "" && !ok9RouterDB(db)) {
		db = detectDB()
	}
	if db == "" {
		db = default9RouterDBPath()
		fmt.Fprintf(os.Stderr, "9Router DB not found. Set db_path in %s\n", cfgPath)
	}

	logDir := firstNonEmpty(os.Getenv("NRTUI_LOG_DIR"), cfg["log_dir"])
	if logDir == "" {
		logDir = filepath.Join(appDir, ".tui-logs")
	} else {
		logDir = resolveAppPath(expandConfigPath(logDir))
	}
	apiBase := firstNonEmpty(os.Getenv("NRTUI_API"), cfg["api_base"])
	if apiBase == "" {
		apiBase = "http://localhost:20128"
	}

	cfg["db_path"] = portableConfigPath(db)
	cfg["log_dir"] = localConfigPath(logDir)
	cfg["api_base"] = apiBase
	cfg["project_dir"] = "."
	if cfg["accounts_path"] == "" || strings.Contains(filepath.Clean(cfg["accounts_path"]), string(os.PathSeparator)+"accounts") {
		cfg["accounts_path"] = filepath.Join(appDir, ".accounts") + string(os.PathSeparator)
	}
	accountsPath := resolveAppPath(expandConfigPath(cfg["accounts_path"]))
	cfg["accounts_path"] = localConfigPath(accountsPath) + string(os.PathSeparator)
	if cfg["dev_mode"] == "" || !strings.HasPrefix(filepath.Clean(db), filepath.Clean(filepath.Join(appDir, ".dev"))+string(os.PathSeparator)) {
		cfg["dev_mode"] = "false"
	}
	_ = saveINI(cfgPath, cfg)

	_ = os.MkdirAll(logDir, 0755)
	_ = os.Setenv("NRTUI_LOG_DIR", logDir)
	_ = os.Setenv("NRTUI_API", apiBase)
	_ = os.Setenv("NRTUI_DEV_MODE", cfg["dev_mode"])
	if exe, err := os.Executable(); err == nil {
		_ = os.Setenv("NRTUI_BINARY_PATH", exe)
	}
	_ = os.Setenv("NRTUI_RELEASE_URL", "https://github.com/achrllrogia45/9rtui/releases/latest")
	if strings.TrimSpace(os.Getenv("NRTUI_ACCOUNTS_PATH")) == "" {
		_ = os.Setenv("NRTUI_ACCOUNTS_PATH", accountsPath+string(os.PathSeparator))
	}
	return cfg, db, nil
}

func checkDB(path string) error {
	path = resolveAppPath(expandConfigPath(path))
	if _, err := os.Stat(path); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", sqliteFileDSN(path, "mode=ro&_busy_timeout=10000"))
	if err != nil {
		return err
	}
	defer db.Close()
	var status string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&status); err != nil {
		return err
	}
	if strings.ToLower(strings.TrimSpace(status)) != "ok" {
		return fmt.Errorf("sqlite quick_check failed: %s", status)
	}
	return nil
}

func runTUI(db string) {
	if _, err := os.Stat(db); err != nil {
		fmt.Fprintf(os.Stderr, "DB not found: %s\nSet db_path in %s\n", db, filepath.Join(appDir, "9rtui.ini"))
		os.Exit(1)
	}
	p := tea.NewProgram(tui.New(db), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func handleWeb(args []string, port int, db string) {
	cmd := "start"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "start":
		runWebForeground("127.0.0.1", port, db)
	case "expose":
		runWebForeground("0.0.0.0", port, db)
	case "serve":
		host := "127.0.0.1"
		if len(args) > 1 {
			host = args[1]
		}
		runWebServer(host, port, db)
	case "stop":
		if err := web.StopHard(appDir); err != nil {
			fmt.Fprintln(os.Stderr, "web stop failed:", err)
			os.Exit(1)
		}
		fmt.Println("9rtui stopped")
	case "restart":
		_ = web.StopHard(appDir)
		runWebForeground("127.0.0.1", port, db)
	default:
		fmt.Println("usage: 9rtui [--port N] web [start|expose|stop|restart]")
		os.Exit(1)
	}
}

func runWebForeground(host string, port int, db string) {
	s := web.New(web.Config{AppDir: appDir, DBPath: db, Host: host, Port: port})
	if err := s.Auth().EnsurePassword(promptPassword); err != nil {
		fmt.Fprintln(os.Stderr, "password setup failed:", err)
		os.Exit(1)
	}
	urlHost := host
	if host == "0.0.0.0" {
		urlHost = "localhost"
	}
	fmt.Printf("\n9rtui web running at http://%s:%d\n", urlHost, port)
	fmt.Println("1) Send to background")
	fmt.Println("2) Exit / Ctrl+C / Ctrl+D")
	fmt.Println()
	go func() {
		if err := s.Serve(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, "web server:", err)
		}
	}()
	_ = web.WritePID(appDir, port)
	defer web.RemovePID(appDir)

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("choice> ")
		line, err := reader.ReadString('\n')
		if err != nil {
			_ = s.Shutdown(context.Background())
			return
		}
		switch strings.TrimSpace(line) {
		case "1":
			if err := sendWebBackground(host, port); err != nil {
				fmt.Fprintln(os.Stderr, "background failed:", err)
				continue
			}
			_ = s.Shutdown(context.Background())
			fmt.Println("9rtui web sent to background")
			return
		case "2", "q", "quit", "exit":
			_ = s.Shutdown(context.Background())
			return
		}
	}
}

func runWebServer(host string, port int, db string) {
	s := web.New(web.Config{AppDir: appDir, DBPath: db, Host: host, Port: port})
	if err := s.Auth().EnsurePassword(promptPassword); err != nil {
		fmt.Fprintln(os.Stderr, "password setup failed:", err)
		os.Exit(1)
	}
	_ = web.WritePID(appDir, port)
	defer web.RemovePID(appDir)
	if err := s.Serve(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func sendWebBackground(host string, port int) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	cmd := exec.Command(exe, "--port", strconv.Itoa(port), "web", "serve", host)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	return cmd.Start()
}

func promptPassword() (string, error) {
	fmt.Println("Set 9rtui web key. Stored as plain WEB_PASS in", filepath.Join(appDir, ".env"))
	fmt.Print("WEB_PASS: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line), err
}

func detectAppDir() string {
	exe, err := os.Executable()
	if err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		if dir := filepath.Dir(exe); dir != "" && dir != "." {
			return dir
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

func migrateLocalDirs(base string) error {
	pairs := [][2]string{{filepath.Join(base, "accounts"), filepath.Join(base, ".accounts")}, {filepath.Join(base, "tui-logs"), filepath.Join(base, ".tui-logs")}, {filepath.Join(base, "dev"), filepath.Join(base, ".dev")}, {filepath.Join(base, "reports"), filepath.Join(base, ".reports")}}
	for _, pair := range pairs {
		oldPath, newPath := pair[0], pair[1]
		if _, err := os.Stat(oldPath); err == nil {
			if _, err := os.Stat(newPath); os.IsNotExist(err) {
				if err := os.Rename(oldPath, newPath); err != nil {
					return err
				}
			} else if err := moveDirContents(oldPath, newPath); err != nil {
				return err
			}
		}
	}
	for _, dir := range []string{filepath.Join(base, ".accounts"), filepath.Join(base, ".tui-logs"), filepath.Join(base, ".tui-logs", "full-backups")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	fixLocalOwnership(base)
	for _, name := range []string{"DB-HEALTH-CHECK.md", "FIX-SUMMARY.md"} {
		oldPath := filepath.Join(base, name)
		newPath := filepath.Join(base, ".reports", name)
		if _, err := os.Stat(oldPath); err == nil {
			_ = os.MkdirAll(filepath.Join(base, ".reports"), 0755)
			if _, err := os.Stat(newPath); os.IsNotExist(err) {
				_ = os.Rename(oldPath, newPath)
			}
		}
	}
	envPath := filepath.Join(base, ".env")
	if _, err := os.Stat(envPath); os.IsNotExist(err) {
		if err := os.WriteFile(envPath, []byte("WEB_PASS=\n"), 0600); err != nil {
			return err
		}
	}
	return nil
}

func fixLocalOwnership(base string) {
	if runtime.GOOS == "windows" {
		return
	}
	uid := os.Getenv("SUDO_UID")
	gid := os.Getenv("SUDO_GID")
	if uid == "" || gid == "" {
		return
	}
	cmd := exec.Command("chown", "-R", uid+":"+gid, base)
	_ = cmd.Run()
}

func moveDirContents(oldPath, newPath string) error {
	entries, err := os.ReadDir(oldPath)
	if err != nil {
		return nil
	}
	if err := os.MkdirAll(newPath, 0755); err != nil {
		return err
	}
	for _, e := range entries {
		src := filepath.Join(oldPath, e.Name())
		dst := filepath.Join(newPath, e.Name())
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		if err := os.Rename(src, dst); err != nil {
			return err
		}
	}
	_ = os.Remove(oldPath)
	return nil
}

func detectDB() string {
	candidates := []string{filepath.Join(appDir, ".dev", "data.sqlite"), default9RouterDBPath()}
	for _, p := range candidates {
		if ok9RouterDB(p) {
			return p
		}
	}
	return ""
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

func expandConfigPath(p string) string {
	p = strings.TrimSpace(os.ExpandEnv(p))
	if p == "" {
		return ""
	}
	re := regexp.MustCompile(`%([^%]+)%`)
	p = re.ReplaceAllStringFunc(p, func(s string) string {
		name := strings.Trim(s, "%")
		if v := os.Getenv(name); v != "" {
			return v
		}
		return s
	})
	return filepath.Clean(p)
}

func resolveAppPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(appDir, p)
}

func localConfigPath(p string) string {
	p = filepath.Clean(strings.TrimSpace(p))
	if p == "" {
		return p
	}
	if rel, err := filepath.Rel(appDir, p); err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
		return "." + string(os.PathSeparator) + rel
	}
	return p
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

func portableConfigPath(p string) string {
	p = filepath.Clean(strings.TrimSpace(p))
	if runtime.GOOS == "windows" {
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			base := filepath.Clean(appData) + string(os.PathSeparator)
			if strings.HasPrefix(strings.ToLower(p), strings.ToLower(base)) {
				return `%APPDATA%` + string(os.PathSeparator) + strings.TrimPrefix(p, base)
			}
		}
		if home := strings.TrimSpace(os.Getenv("USERPROFILE")); home != "" {
			base := filepath.Clean(home) + string(os.PathSeparator)
			if strings.HasPrefix(strings.ToLower(p), strings.ToLower(base)) {
				return `%USERPROFILE%` + string(os.PathSeparator) + strings.TrimPrefix(p, base)
			}
		}
	}
	return p
}

func ok9RouterDB(path string) bool {
	path = expandConfigPath(path)
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && strings.HasSuffix(strings.ToLower(path), ".sqlite")
}

func loadINI(path string) map[string]string {
	m := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return m
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}

func saveINI(path string, m map[string]string) error {
	var b strings.Builder
	b.WriteString("# 9rtui settings\n")
	b.WriteString("[paths]\n")
	for _, k := range []string{"project_dir", "db_path", "log_dir", "api_base", "accounts_path", "dev_mode"} {
		if v := m[k]; v != "" {
			b.WriteString(k + " = " + v + "\n")
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0644)
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return strings.TrimSpace(x)
		}
	}
	return ""
}

func waitBrief() { time.Sleep(200 * time.Millisecond) }
