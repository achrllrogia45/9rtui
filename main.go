package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"net"
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
	"github.com/9rtui/9rtui/internal/routerapi"
	"github.com/9rtui/9rtui/internal/tui"
	"github.com/9rtui/9rtui/internal/web"
	tea "github.com/charmbracelet/bubbletea"
	_ "modernc.org/sqlite"
)

var appDir = detectAppDir()

var version = "dev"
var commit = "unknown"
var buildDate = "unknown"

func portFlagPresent(args []string) bool {
	for i, a := range args {
		if a == "--port" || a == "-port" {
			return i+1 < len(args)
		}
		if strings.HasPrefix(a, "--port=") || strings.HasPrefix(a, "-port=") {
			return true
		}
	}
	return false
}

func main() {
	port := flag.Int("port", 20129, "web server port")
	showVersion := flag.Bool("version", false, "print version and exit")
	showVersionShort := flag.Bool("v", false, "print version and exit")
	flag.Usage = usage
	portOnlyWeb := portFlagPresent(os.Args[1:])
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
		case "doctor":
			if err := runDoctor(cfg, db); err != nil {
				os.Exit(1)
			}
			return
		case "official-export":
			if err := officialExportCLI(args[1:], cfg); err != nil {
				fmt.Fprintln(os.Stderr, "official-export failed:", err)
				os.Exit(1)
			}
			return
		case "official-import":
			if err := officialImportCLI(args[1:], db); err != nil {
				fmt.Fprintln(os.Stderr, "official-import failed:", err)
				os.Exit(1)
			}
			return
		case "api-key":
			if err := apiKeyCLI(args[1:], db); err != nil {
				fmt.Fprintln(os.Stderr, "api-key failed:", err)
				os.Exit(1)
			}
			return
		case "import-file":
			if err := importFileCLI(args[1:], db); err != nil {
				fmt.Fprintln(os.Stderr, "import-file failed:", err)
				os.Exit(1)
			}
			return
		case "check-db":
			if err := checkDB(db); err != nil {
				fmt.Fprintln(os.Stderr, "DB FAILED:", err)
				os.Exit(1)
			}
			fmt.Println("DB OK:", db)
			return
		case "index":
			msg, err := repo.New(db).Reindex()
			if err != nil {
				fmt.Fprintln(os.Stderr, "INDEX FAILED:", err)
				os.Exit(1)
			}
			fmt.Println(msg)
			return
		case "vacuum":
			msg, err := repo.New(db).Vacuum()
			if err != nil {
				fmt.Fprintln(os.Stderr, "VACUUM FAILED:", err)
				os.Exit(1)
			}
			fmt.Println(msg)
			return
		case "restart":
			_ = web.StopHard(appDir)
			handleWeb([]string{"start"}, *port, db)
			return
		default:
			fmt.Fprintln(os.Stderr, "unknown command:", args[0])
			usage()
			os.Exit(2)
		}
	}
	_ = cfg
	if len(args) == 0 && portOnlyWeb {
		handleWeb(nil, *port, db)
		return
	}
	runTUI(db)
}

func runDoctor(cfg map[string]string, dbPath string) error {
	hadErr := false
	ok := func(name string, err error) {
		if err != nil {
			fmt.Printf("FAIL %-18s %v\n", name, err)
			hadErr = true
			return
		}
		fmt.Printf("OK   %s\n", name)
	}
	fmt.Printf("9rtui %s (commit %s, built %s)\n", version, commit, buildDate)
	fmt.Println("db:", dbPath)
	fmt.Println("api:", cfg["api_base"])
	ok("db", checkDB(dbPath))
	if _, err := routerapi.CLIToken(); err != nil {
		ok("cli token", err)
	} else {
		ok("cli token", nil)
	}
	c, err := routerapi.New(cfg["api_base"])
	if err != nil {
		ok("official api", err)
	} else {
		b, err := c.ExportDatabase(context.Background())
		ok("official api", err)
		if err == nil {
			fmt.Printf("OK   official counts providerConnections=%d apiKeys=%d\n", len(routerapi.ProviderConnections(b)), len(routerapi.APIKeys(b)))
		}
	}
	keys, err := repo.New(dbPath).ListAPIKeys()
	ok("apiKeys table", err)
	if err == nil {
		fmt.Printf("OK   apiKeys rows=%d\n", len(keys))
	}
	if hadErr {
		return fmt.Errorf("doctor found failures")
	}
	return nil
}

func officialExportCLI(args []string, cfg map[string]string) error {
	out := "official-backup-" + time.Now().Format("20060102-1504") + ".json"
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		out = args[0]
	}
	c, err := routerapi.New(cfg["api_base"])
	if err != nil {
		return err
	}
	b, err := c.ExportDatabase(context.Background())
	if err != nil {
		return err
	}
	bb, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, bb, 0600); err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func officialImportCLI(args []string, dbPath string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: 9rtui official-import file.json")
	}
	r := repo.New(dbPath)
	added, err := r.ImportAccountBundle(args[0])
	if err != nil {
		return err
	}
	fmt.Println("imported", args[0])
	fmt.Println("added", added)
	return nil
}

func importFileCLI(args []string, dbPath string) error {
	fs := flag.NewFlagSet("import-file", flag.ContinueOnError)
	provider := fs.String("provider", "kiro", "provider: kiro|codex|antigravity")
	file := fs.String("file", "", "accounts json file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" && fs.NArg() > 0 {
		*file = fs.Arg(0)
	}
	if *file == "" {
		return fmt.Errorf("usage: 9rtui import-file -provider kiro -file accounts.json")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	res, err := importer.RunProvider(ctx, importer.ImportOptions{AccountsPath: *file, DBPath: dbPath, DoImport: true, DryRun: false, ActiveOnly: false, IncludeInactive: true, OnlyAvailable: false, Parallel: 5}, *provider)
	if err != nil {
		return err
	}
	fmt.Printf("imported provider=%s selected=%d ok=%d failed=%d\n", *provider, res.Selected, res.OK, res.Fail)
	return nil
}

func apiKeyCLI(args []string, dbPath string) error {
	r := repo.New(dbPath)
	if len(args) == 0 || args[0] == "list" {
		keys, err := r.ListAPIKeys()
		if err != nil {
			return err
		}
		for _, k := range keys {
			fmt.Printf("%s\t%s\t%v\t%s\t%s\n", k.ID, repo.MaskKey(k.Key), k.IsActive, k.Name, k.CreatedAt)
		}
		return nil
	}
	switch args[0] {
	case "create":
		name, key := "", ""
		if len(args) > 1 {
			name = args[1]
		}
		if len(args) > 2 {
			key = args[2]
		}
		k, err := r.CreateAPIKey(name, key)
		if err != nil {
			return err
		}
		fmt.Printf("created\nid: %s\nname: %s\nkey: %s\n", k.ID, k.Name, k.Key)
	case "edit":
		if len(args) < 3 {
			return fmt.Errorf("usage: 9rtui api-key edit <id> <name> [key]")
		}
		key := ""
		if len(args) > 3 {
			key = args[3]
		}
		return r.UpdateAPIKey(args[1], args[2], key)
	case "toggle":
		if len(args) < 2 {
			return fmt.Errorf("usage: 9rtui api-key toggle <id>")
		}
		return r.ToggleAPIKey(args[1])
	case "delete":
		if len(args) < 2 {
			return fmt.Errorf("usage: 9rtui api-key delete <id>")
		}
		return r.DeleteAPIKey(args[1])
	default:
		return fmt.Errorf("usage: 9rtui api-key list|create [name] [key]|edit <id> <name> [key]|toggle <id>|delete <id>")
	}
	return nil
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
	fmt.Fprintln(out, "  9rtui doctor                  DB/API/token/API-key diagnostics")
	fmt.Fprintln(out, "  9rtui official-export [file]  export official 9Router backup JSON")
	fmt.Fprintln(out, "  9rtui official-import file    import official 9Router backup JSON")
	fmt.Fprintln(out, "  9rtui api-key list|create|edit|toggle|delete")
	fmt.Fprintln(out, "  9rtui import-file -provider kiro -file accounts.json")
	fmt.Fprintln(out, "  9rtui check-db                verify configured 9Router DB")
	fmt.Fprintln(out, "  9rtui index                   force-stop 9Router, daily backup, REINDEX")
	fmt.Fprintln(out, "  9rtui vacuum                  force-stop 9Router, daily backup, VACUUM")
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
	fmt.Println("auto_backup_method:", cfg["auto_backup_method"])
	fmt.Println("snap_backup_method:", cfg["snap_backup_method"])
	fmt.Println("version:", version)
	fmt.Println("commit:", commit)
	fmt.Println("built:", buildDate)
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
	autoMethod := firstNonEmpty(os.Getenv("NRTUI_AUTO_BACKUP_METHOD"), cfg["auto_backup_method"])
	if autoMethod == "" {
		autoMethod = "none"
	}
	snapMethod := firstNonEmpty(os.Getenv("NRTUI_SNAP_BACKUP_METHOD"), cfg["snap_backup_method"])
	if snapMethod == "" {
		snapMethod = "none"
	}

	cfg["db_path"] = portableConfigPath(db)
	cfg["log_dir"] = localConfigPath(logDir)
	cfg["api_base"] = apiBase
	cfg["auto_backup_method"] = autoMethod
	cfg["snap_backup_method"] = snapMethod
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
	_ = os.Setenv("NRTUI_AUTO_BACKUP_METHOD", cfg["auto_backup_method"])
	_ = os.Setenv("NRTUI_SNAP_BACKUP_METHOD", cfg["snap_backup_method"])
	_ = os.Setenv("NRTUI_CONFIG_PATH", cfgPath)
	if exe, err := os.Executable(); err == nil {
		_ = os.Setenv("NRTUI_BINARY_PATH", exe)
	}
	_ = os.Setenv("NRTUI_RELEASE_URL", "https://github.com/achrllrogia45/9rtui/releases/tag/v0.2.0-beta.1")
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

func runTUI(db string) {
	if _, err := os.Stat(db); err != nil {
		fmt.Fprintf(os.Stderr, "DB not found: %s\nSet db_path in %s\n", db, filepath.Join(appDir, "9rtui.ini"))
		os.Exit(1)
	}
	p := tea.NewProgram(tui.New(db, version, commit, buildDate), tea.WithAltScreen())
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
		if len(args) > 1 && args[1] == "expose" {
			host = "0.0.0.0"
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
	urlHost := displayHost(host)
	fmt.Printf("\n9rtui web running at http://%s:%d\n", urlHost, port)
	fmt.Println("\nChoose:")
	fmt.Println("  1) Keep running in background")
	fmt.Println("  2) Stop and exit")
	fmt.Println("\nClose this window or press Ctrl+C to stop foreground server.")
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
	urlHost := displayHost(host)
	fmt.Printf("9rtui web running at http://%s:%d\n", urlHost, port)
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
	mode := "start"
	if host == "0.0.0.0" {
		mode = "expose"
	}
	cmd := exec.Command(exe, "--port", strconv.Itoa(port), "web", "serve", mode)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	detachCommand(cmd)
	return cmd.Start()
}

func displayHost(host string) string {
	if host != "0.0.0.0" {
		return host
	}
	if ip := localLANIP(); ip != "" {
		return ip
	}
	return "0.0.0.0"
}

func localLANIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err == nil {
		defer conn.Close()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && addr.IP != nil && !addr.IP.IsLoopback() {
			return addr.IP.String()
		}
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip4 := ip.To4(); ip4 != nil && !ip4.IsLoopback() {
				return ip4.String()
			}
		}
	}
	return ""
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
