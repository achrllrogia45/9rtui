package web

import (
	"context"
	"embed"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

//go:embed static/*
var staticFS embed.FS

type Config struct {
	AppDir string
	DBPath string
	Host   string
	Port   int
}

type Server struct {
	cfg  Config
	auth *Auth
	srv  *http.Server
}

func New(cfg Config) *Server {
	return &Server{cfg: cfg, auth: NewAuth(cfg.AppDir)}
}

func (s *Server) Auth() *Auth { return s.auth }

func (s *Server) Addr() string { return net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port)) }

func (s *Server) Serve() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/logout", s.handleLogout)
	mux.HandleFunc("/ws", s.handleWS)
	s.srv = &http.Server{Addr: s.Addr(), Handler: mux}
	return s.srv.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if !s.auth.OK(r) {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	b, _ := staticFS.ReadFile("static/index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err == nil && s.auth.CheckPassword(r.FormValue("password")) {
			_ = s.auth.Login(w)
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
	}
	b, _ := staticFS.ReadFile("static/login.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.auth.Logout(w, r)
	http.Redirect(w, r, "/login", http.StatusFound)
}

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !s.auth.OK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	exe, _ := os.Executable()
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	cmd := exec.Command(exe, "tui")
	cmd.Env = append(os.Environ(), "NINETUI_DB="+s.cfg.DBPath)
	ptyFile, err := startPTY(cmd)
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("failed to start TUI: "+err.Error()))
		return
	}
	defer ptyFile.Close()
	defer cmd.Process.Kill()

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptyFile.Read(buf)
			if n > 0 {
				_ = conn.WriteMessage(websocket.BinaryMessage, buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if strings.TrimSpace(string(data)) == "\x04" {
			return
		}
		_, _ = ptyFile.Write(data)
	}
}

func WritePID(appDir string, port int) error {
	return os.WriteFile(filepath.Join(appDir, "web.pid"), []byte(fmt.Sprintf("%d\n%d\n", os.Getpid(), port)), 0600)
}

func ReadPID(appDir string) (int, error) {
	b, err := os.ReadFile(filepath.Join(appDir, "web.pid"))
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, fmt.Errorf("empty pid file")
	}
	return strconv.Atoi(fields[0])
}

func RemovePID(appDir string) { _ = os.Remove(filepath.Join(appDir, "web.pid")) }

func Stop(appDir string) error {
	pid, err := ReadPID(appDir)
	if err != nil {
		return err
	}
	return killPID(appDir, pid)
}

func StopHard(appDir string) error {
	var killed bool
	if pid, err := ReadPID(appDir); err == nil {
		if err := killPID(appDir, pid); err == nil {
			killed = true
		}
	}
	pids, _ := findWebPIDs()
	self := os.Getpid()
	for _, pid := range pids {
		if pid == self || pid <= 0 {
			continue
		}
		if err := killPID(appDir, pid); err == nil {
			killed = true
		}
	}
	RemovePID(appDir)
	if !killed {
		return nil
	}
	return nil
}

func killPID(appDir string, pid int) error {
	if runtime.GOOS == "windows" {
		RemovePID(appDir)
		return exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run()
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	_ = p.Signal(os.Interrupt)
	for i := 0; i < 10; i++ {
		if !processAlive(pid) {
			RemovePID(appDir)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = p.Kill()
	RemovePID(appDir)
	return nil
}

func findWebPIDs() ([]int, error) {
	if runtime.GOOS == "windows" {
		return nil, nil
	}
	out, err := exec.Command("ps", "-eo", "pid=,comm=,args=").Output()
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		comm := filepath.Base(fields[1])
		args := strings.Join(fields[2:], " ")
		if comm == "9rtui" && strings.Contains(args, " web serve ") {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func processAlive(pid int) bool {
	if runtime.GOOS == "windows" {
		return exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid)).Run() == nil
	}
	return exec.Command("kill", "-0", strconv.Itoa(pid)).Run() == nil
}

func Copy(w io.Writer, r io.Reader) { _, _ = io.Copy(w, r) }
