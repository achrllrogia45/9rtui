package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/9rtui/9rtui/internal/domain"
	"github.com/9rtui/9rtui/internal/importer"
	"github.com/9rtui/9rtui/internal/repo"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type mode int

const (
	normal mode = iota
	visual
	confirmDelete
	backups
	help
	inspect
	importProvider
	importFilePicker
	importScreen
	confirmImport
	confirmVacuum
	confirmRestore
	exportDone
)

type importProviderOption struct {
	ID          string
	Label       string
	Description string
	Source      string
	Ready       bool
}

type importRowsMsg struct {
	rows []importer.KiroAccount
	err  error
}

type importFilePreviewMsg struct {
	path     string
	accounts []importer.KiroAccount
	text     string
	err      error
}

type restorePreviewMsg struct {
	path string
	text string
	err  error
}

type Model struct {
	r                  *repo.Repo
	accounts           []domain.Account
	providers          []string
	group, cursor      int
	selected           map[string]bool
	focusLeft          bool
	mode               mode
	visualStart        int
	msg                string
	overlayTitle       string
	overlayBody        string
	overlayKind        string
	w, h               int
	search             string
	stateFilter        string
	sortField          string
	sortDesc           bool
	backupCursor       int
	backupInfos        []domain.BackupInfo
	backupScroll       int
	undoCursor         int
	undoInfos          []repo.UndoInfo
	pendingG           bool
	inspectText        string
	inspectIDs         []string
	inspectScroll      int
	importReturn       mode
	importProvider     string
	importProviders    []importProviderOption
	importProviderCur  int
	importFiles        []string
	importFileCursor   int
	importAccountsPath string
	importRows         []importer.KiroAccount
	importCursor       int
	importSelected     map[string]bool
	importVisual       bool
	importVisualStart  int
	importFilter       string
	importSort         string
	pendingImportTest  bool
	importRunning      bool
	importTotal        int
	importDone         int
	importOK           int
	importFail         int
	importErr          int
	importDBCheck      string
	importLabel        string
	vacuumRunning      bool
	vacuumMsg          string
	vacuum9RRunning    bool
	// preview state for restore (backups mode) right pane
	restorePreviewPath   string
	restorePreviewText   string
	restorePreviewScroll int
	// preview state shared by importFilePicker and importScreen right pane
	importPreviewPath     string
	importPreviewAccounts []importer.KiroAccount
	importPreviewText     string
	importPreviewScroll   int
	// pickerFocus 0 = left file list, 1 = right-top accounts preview
	pickerFocus     int
	pickerAccCursor int
}

type importDoneMsg struct {
	label string
	ok    int
	fail  int
	errs  int
	db    string
	err   error
}

type importProgressMsg struct{}

type vacuumDoneMsg struct {
	result string
	err    error
}

var importProg struct {
	sync.Mutex
	running bool
	total   int
	done    int
	ok      int
	fail    int
	errs    int
}

func importTick() tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg { return importProgressMsg{} })
}

func New(dbPath string) Model {
	m := Model{r: repo.New(dbPath), selected: map[string]bool{}, importSelected: map[string]bool{}, focusLeft: true, stateFilter: "off", sortField: "name", importFilter: "all", importSort: "email", importProvider: "kiro"}
	m.refresh()
	return m
}
func (m *Model) refresh() {
	a, err := m.r.ListAccounts()
	if err != nil {
		m.msg = err.Error()
		return
	}
	m.accounts = a
	known, _ := m.r.ListProviderIDs()
	m.providers = repo.ProvidersWithKnown(a, known)
	if m.group >= len(m.providers) {
		m.group = 0
	}
	if m.cursor >= len(m.visible()) {
		m.cursor = max(0, len(m.visible())-1)
	}
	m.msg = fmt.Sprintf("loaded %d accounts", len(a))
}
func (m Model) Init() tea.Cmd { return nil }
func (m Model) visible() []domain.Account {
	gp := "all"
	if len(m.providers) > 0 {
		gp = m.providers[m.group]
	}
	var out []domain.Account
	q := strings.ToLower(m.search)
	for _, a := range m.accounts {
		if gp != "all" && a.Provider != gp {
			continue
		}
		if m.stateFilter == "active" && !a.IsActive {
			continue
		}
		if m.stateFilter == "inactive" && a.IsActive {
			continue
		}
		s := strings.ToLower(a.Name + " " + a.Email + " " + a.Provider)
		if q != "" && !strings.Contains(s, q) {
			continue
		}
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		c := compareAccounts(out[i], out[j], m.sortField)
		if m.sortDesc {
			return c > 0
		}
		return c < 0
	})
	return out
}
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch x := msg.(type) {
	case vacuumDoneMsg:
		m.vacuumRunning = false
		if x.err != nil {
			m.vacuumMsg = "VACUUM failed: " + x.err.Error()
			m.importDBCheck = "failed"
			m.importLabel = "VACUUM"
			m.importDone = -1
			m.importTotal = 0
		} else {
			m.vacuumMsg = x.result
			m.importDBCheck = "ok"
			m.importLabel = "VACUUM"
			m.importOK = 1
			m.importFail = 0
			m.importErr = 0
			m.importTotal = 0
			m.importDone = 1
			m.refresh()
		}
		m.msg = m.vacuumMsg
		return m, nil
	case importDoneMsg:
		m.importRunning = false
		m.importOK = x.ok
		m.importFail = x.fail
		m.importErr = x.errs
		m.importDBCheck = x.db
		m.importDone = x.ok + x.fail + x.errs
		if x.err != nil {
			m.msg = x.label + " failed: " + x.err.Error()
			return m, nil
		}
		m.msg = x.label + " done; reloading import table"
		m.refresh()
		m.importSelected = map[string]bool{}
		m.importVisual = false
		return m.openImportScreen()
	case importProgressMsg:
		importProg.Lock()
		m.importRunning = importProg.running
		m.importTotal = importProg.total
		m.importDone = importProg.done
		m.importOK = importProg.ok
		m.importFail = importProg.fail
		m.importErr = importProg.errs
		running := importProg.running
		importProg.Unlock()
		if running {
			return m, importTick()
		}
		return m, nil
	case importRowsMsg:
		if x.err != nil {
			m.msg = x.err.Error()
		} else {
			m.importRows = x.rows
			m.importCursor = 0
			m.mode = importScreen
			m.msg = fmt.Sprintf("loaded %d %s import rows", len(x.rows), importProviderLabel(m.importProvider))
		}
		return m, nil
	case importFilePreviewMsg:
		m.importPreviewPath = x.path
		m.importPreviewAccounts = x.accounts
		m.importPreviewText = x.text
		m.importPreviewScroll = 0
		m.pickerAccCursor = 0
		return m, nil
	case restorePreviewMsg:
		m.restorePreviewPath = x.path
		if x.err != nil {
			m.restorePreviewText = "(failed to read file: " + x.err.Error() + ")"
		} else {
			m.restorePreviewText = x.text
		}
		m.restorePreviewScroll = 0
		return m, nil
	case tea.WindowSizeMsg:
		m.w = x.Width
		m.h = x.Height
		return m, nil
	case tea.KeyMsg:
		k := x.String()
		if k == "ctrl+q" || k == "ctrl+c" {
			if m.importRunning {
				m.msg = "import running; exit blocked until done"
				return m, nil
			}
			return m, tea.Quit
		}
		switch m.mode {
		case exportDone:
			if k == "enter" || k == "esc" || k == "o" || k == "O" {
				m.clearOverlay()
				m.mode = inspect
			}
			return m, nil
		case confirmDelete:
			if k == "enter" || k == "y" || k == "Y" {
				ids := m.ids()
				n, err := m.r.DeleteAccounts(ids)
				if err != nil {
					m.msg = err.Error()
					m.importDBCheck = "failed: " + err.Error()
					m.importLabel = "DELETE"
					m.importDone = -1
				} else {
					m.selected = map[string]bool{}
					m.refresh()
					m.msg = fmt.Sprintf("deleted %d accounts, undo log saved", n)
					m.importDBCheck = "ok"
					m.importLabel = "DELETE"
					m.importOK = int(n)
					m.importFail = 0
					m.importErr = 0
					m.importTotal = 0
					m.importDone = int(n)
				}
				m.mode = normal
			}
			if k == "esc" {
				m.mode = normal
			}
			return m, nil
		case backups:
			if k == "esc" || k == "q" {
				m.mode = normal
				return m, nil
			}
			maxIdx := len(m.undoInfos) - 1
			if m.pendingG {
				m.pendingG = false
				if k == "g" {
					m.undoCursor = 0
					m.backupScroll = 0
					m.clampBackupScroll()
					return m, m.loadRestorePreviewCmd()
				}
			}
			prevCursor := m.undoCursor
			switch k {
			case "j", "down":
				if m.undoCursor < maxIdx {
					m.undoCursor++
				}
			case "k", "up":
				if m.undoCursor > 0 {
					m.undoCursor--
				}
			case "G":
				m.undoCursor = maxIdx
			case "g":
				m.pendingG = true
			case "J":
				// scroll right preview down
				lines := strings.Split(m.restorePreviewText, "\n")
				bodyH := max(4, m.h-13)
				maxScroll := max(0, len(lines)-bodyH)
				if m.restorePreviewScroll < maxScroll {
					m.restorePreviewScroll++
				}
				return m, nil
			case "K":
				if m.restorePreviewScroll > 0 {
					m.restorePreviewScroll--
				}
				return m, nil
			}
			m.clampBackupScroll()
			if (k == "R" || k == "r") && len(m.undoInfos) > 0 {
				m.mode = confirmRestore
			}
			if k == "F" {
				if err := m.r.BackupRotate(); err != nil {
					m.msg = err.Error()
				} else {
					m.msg = "full snapshot created (.bak rotated)"
				}
				ui, _ := m.r.UndoLogs()
				m.undoInfos = ui
			}
			if k == "C" {
				m.vacuum9RRunning = is9RouterRunning()
				m.vacuumMsg = ""
				m.mode = confirmVacuum
			}
			if k == "I" || k == "i" {
				return m.openImportProvider()
			}
			// trigger restore preview reload if cursor changed or first view
			if m.undoCursor != prevCursor || (len(m.undoInfos) > 0 && m.restorePreviewPath != m.undoInfos[m.undoCursor].Path) {
				return m, m.loadRestorePreviewCmd()
			}
			return m, nil
		case importProvider:
			return m.updateImportProvider(k)
		case importFilePicker:
			return m.updateImportFilePicker(k)
		case importScreen:
			return m.updateImport(k)
		case confirmImport:
			if k == "esc" || k == "n" || k == "N" {
				m.mode = importScreen
				return m, nil
			}
			if k == "enter" || k == "y" || k == "Y" {
				ids := m.importIDs()
				limit := 0
				label := strings.ToLower(importProviderLabel(m.importProvider)) + " import selected"
				if m.pendingImportTest {
					limit = 5
					label = strings.ToLower(importProviderLabel(m.importProvider)) + " test import selected"
				}
				m.pendingImportTest = false
				m.mode = importScreen
				m.importRunning = true
				m.importTotal = len(ids)
				m.importDone, m.importOK, m.importFail, m.importErr = 0, 0, 0, 0
				m.importDBCheck = ""
				m.importLabel = "IMPORT"
				importProg.Lock()
				importProg.running = true
				importProg.total = len(ids)
				importProg.done, importProg.ok, importProg.fail, importProg.errs = 0, 0, 0, 0
				importProg.Unlock()
				return m, tea.Batch(m.importCmd(label, ids, limit, true), importTick())
			}
			return m, nil
		case confirmRestore:
			if k == "esc" || k == "n" {
				m.mode = backups
				return m, nil
			}
			if k == "enter" || k == "y" || k == "Y" {
				p := m.undoInfos[m.undoCursor].Path
				if n, err := m.r.RestoreUndo(p); err != nil {
					m.msg = err.Error()
					m.importDBCheck = "failed: " + err.Error()
					m.importLabel = "RESTORE"
					m.importDone = -1
					m.importTotal = 0
					m.importOK = 0
					m.importFail = 0
					m.importErr = 0
				} else {
					m.refresh()
					m.msg = fmt.Sprintf("restored %d account rows from undo log", n)
					m.importDBCheck = "ok"
					m.importLabel = "RESTORE"
					m.importOK = int(n)
					m.importFail = 0
					m.importErr = 0
					m.importTotal = 0
					m.importDone = int(n)
				}
				m.mode = backups
				return m, nil
			}
			return m, nil
		case confirmVacuum:
			if k == "esc" || k == "n" {
				m.mode = backups
				return m, nil
			}
			if (k == "enter" || k == "y") && !m.vacuumRunning {
				m.vacuumRunning = true
				m.vacuumMsg = "vacuuming..."
				m.mode = backups
				return m, m.vacuumCmd()
			}
			return m, nil
		case help:
			if k == "esc" || k == "?" {
				m.mode = normal
			}
			return m, nil
		case inspect:
			if k == "esc" || k == "q" {
				m.mode = normal
				return m, nil
			}
			maxScroll := max(0, len(strings.Split(m.inspectText, "\n"))-(m.h-9))
			if m.pendingG {
				m.pendingG = false
				if k == "g" {
					m.inspectScroll = 0
					return m, nil
				}
			}
			switch k {
			case "j", "down":
				if m.inspectScroll < maxScroll {
					m.inspectScroll++
				}
			case "k", "up":
				if m.inspectScroll > 0 {
					m.inspectScroll--
				}
			case "G":
				m.inspectScroll = maxScroll
			case "g":
				m.pendingG = true
			case "e":
				path, err := m.r.ExportAccounts(m.inspectIDs)
				if err != nil {
					m.msg = err.Error()
					return m, nil
				}
				name := filepath.Base(path)
				m.msg = "exported " + name
				m.setOverlay(" Exported ", "exported "+name, "ok")
				m.mode = exportDone
				return m, nil
			}
			return m, nil
		}

		vis := m.visible()
		if m.pendingG {
			m.pendingG = false
			if k == "g" {
				if m.focusLeft {
					m.group = 0
					m.cursor = 0
				} else {
					m.cursor = 0
				}
				return m, nil
			}
		}
		switch k {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "?":
			m.mode = help
		case "enter", "i":
			ids := m.inspectScopeIDs(vis)
			if len(ids) == 0 {
				m.msg = "nothing to inspect"
				break
			}
			xs, err := m.r.GetAccounts(ids)
			if err != nil {
				m.msg = err.Error()
				break
			}
			b, _ := json.MarshalIndent(xs, "", "  ")
			m.inspectText = string(b)
			m.inspectIDs = ids
			m.inspectScroll = 0
			m.mode = inspect
		case "h", "left":
			m.focusLeft = true
		case "l", "right":
			m.focusLeft = false
		case "r":
			m.refresh()
		case "I":
			return m.openImportProvider()
		case "b":
			ui, err := m.r.UndoLogs()
			if err != nil {
				m.msg = err.Error()
			} else {
				m.undoInfos = ui
				m.undoCursor = 0
				m.backupScroll = 0
				m.mode = backups
				m.restorePreviewPath = ""
				m.restorePreviewText = ""
				m.restorePreviewScroll = 0
				if len(ui) > 0 {
					return m, m.loadRestorePreviewCmd()
				}
			}
		case "d":
			if len(m.ids()) > 0 {
				m.mode = confirmDelete
			} else {
				m.msg = "no selection"
			}
		case "O":
			ids := m.ids()
			if len(ids) == 0 {
				m.msg = "no selection"
				break
			}
			targetActive := m.toggleTargetActive(ids)
			n, err := m.r.SetAccountsActive(ids, targetActive)
			if err != nil {
				m.msg = err.Error()
				m.importDBCheck = "failed: " + err.Error()
				m.importLabel = "TOGGLE"
				m.importDone = -1
				break
			}
			m.refresh()
			state := "inactive"
			if targetActive {
				state = "active"
			}
			m.msg = fmt.Sprintf("set %d selected accounts %s", n, state)
			m.importDBCheck = "ok"
			m.importLabel = "TOGGLE"
			m.importOK = int(n)
			m.importFail = 0
			m.importErr = 0
			m.importTotal = 0
			m.importDone = int(n)
		case "f":
			switch m.stateFilter {
			case "off", "":
				m.stateFilter = "active"
			case "active":
				m.stateFilter = "inactive"
			default:
				m.stateFilter = "off"
			}
			m.cursor = 0
			m.msg = "filter: " + m.stateFilter
		case "s":
			switch m.sortField {
			case "name", "":
				m.sortField = "provider"
			case "provider":
				m.sortField = "state"
			case "state":
				m.sortField = "priority"
			case "priority":
				m.sortField = "updated"
			default:
				m.sortField = "name"
			}
			m.cursor = 0
			m.msg = "sort: " + m.sortField + " " + m.sortDirLabel()
		case "S":
			m.sortDesc = !m.sortDesc
			m.cursor = 0
			m.msg = "sort: " + m.sortField + " " + m.sortDirLabel()
		case "a":
			for _, a := range vis {
				m.selected[a.ID] = true
			}
		case "A":
			m.selected = map[string]bool{}
		case " ":
			if !m.focusLeft && len(vis) > 0 {
				id := vis[m.cursor].ID
				m.selected[id] = !m.selected[id]
			}
		case "v":
			if m.mode == visual {
				m.selectRange()
				m.mode = normal
			} else if !m.focusLeft {
				m.mode = visual
				m.visualStart = m.cursor
			}
		case "g":
			m.pendingG = true
		case "G":
			if m.focusLeft {
				m.group = len(m.providers) - 1
			} else {
				m.cursor = max(0, len(vis)-1)
			}
		case "j", "down":
			if m.focusLeft {
				if m.group < len(m.providers)-1 {
					m.group++
					m.cursor = 0
				}
			} else if m.cursor < len(vis)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.focusLeft {
				if m.group > 0 {
					m.group--
					m.cursor = 0
				}
			} else if m.cursor > 0 {
				m.cursor--
			}
		case "esc":
			m.mode = normal
			m.search = ""
		}
		if k == "g" { /* single g noop; gg handled poorly by Bubbletea simple mode */
		}
	}
	return m, nil
}
func (m Model) loadRestorePreviewCmd() tea.Cmd {
	if len(m.undoInfos) == 0 {
		return nil
	}
	path := m.undoInfos[m.undoCursor].Path
	return func() tea.Msg {
		b, err := os.ReadFile(path)
		text := ""
		if err == nil {
			text = string(b)
			// pretty-print JSON if parseable
			var v any
			if json.Unmarshal(b, &v) == nil {
				if pb, perr := json.MarshalIndent(v, "", "  "); perr == nil {
					text = string(pb)
				}
			}
		}
		return restorePreviewMsg{path: path, text: text, err: err}
	}
}

func (m Model) loadFilePreviewCmd(path string) tea.Cmd {
	provider := m.importProvider
	return func() tea.Msg {
		accs, _ := importer.LoadProviderAccounts(path, provider)
		b, err := os.ReadFile(path)
		text := ""
		if err == nil {
			text = string(b)
			// pretty-print JSON if parseable, otherwise leave raw
			if strings.HasSuffix(strings.ToLower(path), ".json") {
				var v any
				if json.Unmarshal(b, &v) == nil {
					if pb, perr := json.MarshalIndent(v, "", "  "); perr == nil {
						text = string(pb)
					}
				}
			}
		}
		return importFilePreviewMsg{path: path, accounts: accs, text: text, err: err}
	}
}

func (m *Model) clampBackupScroll() {
	listH := max(6, m.h-10)
	maxScroll := max(0, len(m.undoInfos)-listH)
	m.backupScroll = clamp(m.backupScroll, 0, maxScroll)
	if m.undoCursor < m.backupScroll {
		m.backupScroll = m.undoCursor
	}
	if m.undoCursor >= m.backupScroll+listH {
		m.backupScroll = m.undoCursor - listH + 1
	}
}

func (m *Model) selectRange() {
	vis := m.visible()
	a, b := m.visualStart, m.cursor
	if a > b {
		a, b = b, a
	}
	for i := a; i <= b && i < len(vis); i++ {
		m.selected[vis[i].ID] = true
	}
}
func (m Model) ids() []string {
	var ids []string
	for id, on := range m.selected {
		if on {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func (m Model) selectedAllProvider(provider string) bool {
	want := map[string]bool{}
	for _, id := range m.ids() {
		want[id] = true
	}
	if len(want) == 0 {
		return false
	}
	for _, a := range m.accounts {
		if want[a.ID] && a.Provider != provider {
			return false
		}
	}
	return true
}

func (m Model) mainHeadLabel(label, field string) string {
	if m.sortField == field {
		return ok.Render(label)
	}
	return muted.Render(label)
}

func (m Model) sortDirLabel() string {
	if m.sortDesc {
		return "desc"
	}
	return "asc"
}

func isMediaProviderName(p string) bool {
	p = strings.ToLower(strings.TrimSpace(p))
	for _, x := range repo.MediaProviders {
		if p == x {
			return true
		}
	}
	return false
}

func providerDisplayName(p string) string {
	if p == "all" {
		return "all"
	}
	return p
}

func providerChip(provider string) lipgloss.Style {
	bg := providerColor(provider)
	fg := "#FFFFFF"
	if strings.EqualFold(provider, "all") {
		fg = "#111827"
	}
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(fg)).Background(lipgloss.Color(bg))
}

func providerColor(provider string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	switch p {
	case "all", "":
		return "#E5E7EB" // neutral light
	case "gemini", "google-gemini", "google":
		return "#0891B2" // sea blue / cyan
	case "codex", "openai-codex", "openai", "chatgpt":
		return "#000000" // OpenAI black
	case "kiro":
		return "#7C3AED" // purple
	case "antigravity", "anti-gravity", "google-antigravity":
		return "#EA4335" // Google red
	case "claude", "anthropic":
		return "#D97757" // Claude warm orange
	case "ollama":
		return "#1F2937" // charcoal
	case "deepseek":
		return "#4F46E5" // indigo
	case "grok", "xai", "x-ai":
		return "#111827" // xAI near-black
	case "openrouter", "9router":
		return "#2563EB" // router blue
	case "openai-compatible-chat-321c4852-ea5b-40e9-9cc6-04b83ca221e0":
		return "#0EA5E9" // Tinggal Colok / sky
	case "openai-compatible-chat-57e06099-9ec5-422c-80bb-67ad9520f1fe":
		return "#9333EA" // enowxai / violet
	case "openai-compatible-chat-8c7482a5-8295-414f-88c0-245890130531":
		return "#14B8A6" // custom chat / teal
	case "openai-compatible-responses-b4e57527-0477-422e-85c1-a724318f468f":
		return "#F43F5E" // custom responses / rose
	case "openai-compatible-chat", "openai-compatible-responses", "openai-compatible":
		return "#64748B" // generic OpenAI-compatible slate
	}
	if strings.HasPrefix(p, "openai-compatible-chat-") {
		return customProviderColor(p, []string{"#0EA5E9", "#9333EA", "#14B8A6", "#8B5CF6", "#06B6D4", "#10B981", "#3B82F6"})
	}
	if strings.HasPrefix(p, "openai-compatible-responses-") {
		return customProviderColor(p, []string{"#F43F5E", "#E11D48", "#EC4899", "#FB7185", "#BE185D"})
	}
	switch p {
	case "qwen", "alibaba":
		return "#FF6A00" // Alibaba orange
	case "mistral":
		return "#FF7000" // Mistral orange
	case "perplexity":
		return "#20B8CD" // Perplexity teal
	case "meta", "llama":
		return "#0668E1" // Meta blue
	case "microsoft", "copilot":
		return "#0078D4" // Microsoft blue
	case "github":
		return "#24292F" // GitHub dark
	case "cursor":
		return "#111111"
	case "windsurf", "codeium":
		return "#00C8FF"
	}
	// Stable generated color for future providers. Keep it vivid but dark enough for white text.
	palette := []string{"#0F766E", "#7C2D12", "#6D28D9", "#BE123C", "#0369A1", "#047857", "#A21CAF", "#B45309", "#4338CA", "#0E7490"}
	h := 0
	for _, r := range p {
		h = (h*31 + int(r)) & 0x7fffffff
	}
	return palette[h%len(palette)]
}

func customProviderColor(provider string, palette []string) string {
	if len(palette) == 0 {
		return "#64748B"
	}
	h := 0
	for _, r := range provider {
		h = (h*31 + int(r)) & 0x7fffffff
	}
	return palette[h%len(palette)]
}

func (m Model) toggleTargetActive(ids []string) bool {
	selected := map[string]bool{}
	for _, id := range ids {
		selected[id] = true
	}
	// If any selected account is inactive, O turns all ON first.
	// If all selected are already active, O turns all OFF.
	for _, a := range m.accounts {
		if selected[a.ID] && !a.IsActive {
			return true
		}
	}
	return false
}

func (m Model) inspectScopeIDs(vis []domain.Account) []string {
	selected := m.ids()
	if len(selected) > 0 {
		return selected
	}
	ids := make([]string, 0, len(vis))
	for _, a := range vis {
		ids = append(ids, a.ID)
	}
	return ids
}

func (m Model) View() string {
	if m.w == 0 {
		m.w = 100
	}
	if m.h == 0 {
		m.h = 30
	}
	var body string
	switch m.mode {
	case help:
		bg := m.mainView()
		popup := helpView()
		body = overlayPopup(m.w, m.h, bg, popup)
	case confirmDelete:
		bg := m.mainView()
		popup := m.confirmDeleteView()
		body = overlayPopup(m.w, m.h, bg, popup)
	case backups:
		body = m.backupView()
	case inspect:
		body = m.inspectView()
	case exportDone:
		bg := lipgloss.Place(m.w, m.h, lipgloss.Left, lipgloss.Top, m.inspectView())
		popup := m.overlayView()
		body = overlayPopup(m.w, m.h, bg, popup)
	case importProvider:
		body = m.importProviderView()
	case importFilePicker:
		body = m.importFilePickerView()
	case importScreen:
		body = m.importView()
	case confirmImport:
		bg := lipgloss.Place(m.w, m.h, lipgloss.Left, lipgloss.Top, m.importView())
		popup := m.confirmImportView()
		body = overlayPopup(m.w, m.h, bg, popup)
	case confirmVacuum:
		bg := lipgloss.Place(m.w, m.h, lipgloss.Left, lipgloss.Top, m.backupView())
		popup := m.vacuumConfirmView()
		body = overlayPopup(m.w, m.h, bg, popup)
	case confirmRestore:
		bg := lipgloss.Place(m.w, m.h, lipgloss.Left, lipgloss.Top, m.backupView())
		popup := m.restoreConfirmView()
		body = overlayPopup(m.w, m.h, bg, popup)
	default:
		leftW := clamp(26, 22, max(22, m.w/3))
		rightW := max(42, m.w-leftW-6)
		vis := m.visible()
		header := m.headerView(len(vis))
		main := lipgloss.JoinHorizontal(lipgloss.Top, m.leftView(leftW), " ", m.rightView(rightW))
		footer := m.footerView()
		body = lipgloss.JoinVertical(lipgloss.Left, header, main, footer)
	}
	if bar := m.importStatusBar(); bar != "" {
		return lipgloss.JoinVertical(lipgloss.Left, bar, body)
	}
	return body
}

func (m Model) mainView() string {
	leftW := clamp(26, 22, max(22, m.w/3))
	rightW := max(42, m.w-leftW-6)
	vis := m.visible()
	header := m.headerView(len(vis))
	main := lipgloss.JoinHorizontal(lipgloss.Top, m.leftView(leftW), " ", m.rightView(rightW))
	footer := m.footerView()
	result := lipgloss.JoinVertical(lipgloss.Left, header, main, footer)
	if bar := m.importStatusBar(); bar != "" {
		result = lipgloss.JoinVertical(lipgloss.Left, bar, result)
	}
	return result
}

func (m *Model) setOverlay(title, body, kind string) {
	m.overlayTitle = title
	m.overlayBody = body
	m.overlayKind = kind
}

func (m *Model) clearOverlay() {
	m.overlayTitle = ""
	m.overlayBody = ""
	m.overlayKind = ""
}

func overlayKindForDB(s string) string {
	if strings.Contains(strings.ToLower(s), "corrupt") || strings.Contains(strings.ToLower(s), "failed") {
		return "danger"
	}
	return "ok"
}

func (m Model) overlayView() string {
	if m.overlayTitle == "" {
		return ""
	}
	style := box
	if m.overlayKind == "danger" {
		style = dangerBox
	}
	w := clamp(m.w-4, 48, 96)
	button := muted.Render("Enter/Esc dismiss")
	if m.overlayKind == "ok" {
		button = selectedChip.Render(" OK ")
	}
	text := brand.Render(" "+m.overlayTitle+" ") + "\n" + m.overlayBody + "\n\n" + lipgloss.PlaceHorizontal(w-4, lipgloss.Center, button)
	return centered(m.w, style.Width(w).Render(text))
}

func (m Model) importStatusBar() string {
	if !m.importRunning && m.importDone == 0 {
		return ""
	}
	label := " IMPORT "
	if m.importLabel != "" {
		label = " " + m.importLabel + " "
	}
	parts := []string{brand.Render(label)}
	if m.importRunning {
		parts = append(parts, chip.Render(fmt.Sprintf(" %d/%d ", m.importDone, m.importTotal)))
	} else if m.importDone == -1 {
		parts = append(parts, errChip.Render(" ERROR "))
	} else {
		parts = append(parts, chip.Render(" DONE "))
	}
	if m.importOK > 0 {
		parts = append(parts, okChip.Render(fmt.Sprintf(" %d OK ", m.importOK)))
	}
	if m.importFail > 0 {
		parts = append(parts, failChip.Render(fmt.Sprintf(" %d Failed ", m.importFail)))
	}
	if m.importErr > 0 {
		parts = append(parts, errChip.Render(fmt.Sprintf(" %d Error ", m.importErr)))
	}
	if m.importDBCheck != "" {
		if overlayKindForDB(m.importDBCheck) == "ok" {
			parts = append(parts, okChip.Render(" DB OK "))
		} else {
			parts = append(parts, errChip.Render(" DB FAILED "))
		}
	}
	if m.importRunning {
		parts = append(parts, muted.Render("  running in background; exit blocked until done"))
	}
	return topbar.Width(max(0, m.w-2)).Render(lipgloss.JoinHorizontal(lipgloss.Center, parts...))
}

func (m Model) headerView(visible int) string {
	gp := "all"
	if len(m.providers) > 0 {
		gp = m.providers[m.group]
	}
	selectedN := len(m.ids())
	selectedStyle := chip
	if selectedN > 0 {
		selectedStyle = selectedChip
	}
	filterStyle := chip
	switch m.stateFilter {
	case "active":
		filterStyle = activeFilterChip
	case "inactive":
		filterStyle = inactiveFilterChip
	}
	sortStyle := ascSortChip
	if m.sortDesc {
		sortStyle = descSortChip
	}
	bar := lipgloss.JoinHorizontal(lipgloss.Center,
		brand.Render(" 9rtui "),
		chip.Render(fmt.Sprintf(" %d accounts ", len(m.accounts))),
		chip.Render(fmt.Sprintf(" %d visible ", visible)),
		selectedStyle.Render(fmt.Sprintf(" %d selected ", selectedN)),
		providerChip(gp).Render(" provider: "+trim(providerDisplayName(gp), 18)+" "),
		filterStyle.Render(" filter: "+m.stateFilter+" "),
		sortStyle.Render(" sort: "+m.sortField+" "+m.sortDirLabel()+" "),
	)
	status := muted.Render("  " + m.msg)
	return topbar.Width(max(0, m.w-2)).Render(lipgloss.JoinVertical(lipgloss.Left, bar, status))
}

func (m Model) footerView() string {
	keys := " h/l pane  j/k move  gg/G jump  Enter/i inspect  I import  Space select  v visual  O on/off  f filter  s/S sort  d delete  b recovery  r refresh  ? help  q quit "
	return footer.Width(max(0, m.w-2)).Render(keys)
}

func (m Model) importCmd(label string, ids []string, limit int, doImport bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		res, err := importer.RunProvider(ctx, importer.ImportOptions{AccountsPath: m.importAccountsPath, DBPath: m.r.Path, DoImport: doImport, DryRun: !doImport, ActiveOnly: false, IncludeInactive: true, OnlyAvailable: false, IDs: ids, Limit: limit, Parallel: 5, Progress: func(ir importer.ImportResult) {
			importProg.Lock()
			importProg.done++
			if ir.HTTPStatus == 0 && ir.Error != "" {
				importProg.errs++
			} else if ir.HTTPStatus >= 200 && ir.HTTPStatus < 300 && ir.Error == "" {
				importProg.ok++
			} else {
				importProg.fail++
			}
			importProg.Unlock()
		}}, m.importProvider)
		importProg.Lock()
		importProg.running = false
		okN, failN, errN := importProg.ok, importProg.fail, importProg.errs
		importProg.Unlock()
		if err != nil {
			return importDoneMsg{label: label, ok: okN, fail: failN, errs: errN, db: res.DBCheck, err: err}
		}
		return importDoneMsg{label: label + " " + importer.Summary(res), ok: okN, fail: failN, errs: errN, db: res.DBCheck, err: nil}
	}
}

func (m Model) openImportScreen() (tea.Model, tea.Cmd) {
	m.importSelected = map[string]bool{}
	m.importVisual = false
	m.importCursor = 0
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		res, err := importer.RunProvider(ctx, importer.ImportOptions{AccountsPath: m.importAccountsPath, DBPath: m.r.Path, DryRun: true, ActiveOnly: false, IncludeInactive: true}, m.importProvider)
		if err != nil {
			return importRowsMsg{err: err}
		}
		return importRowsMsg{rows: res.Rows}
	}
}

func (m Model) updateImport(k string) (tea.Model, tea.Cmd) {
	// Shift+J/K scroll right preview pane from anywhere
	if k == "J" {
		lines := strings.Split(m.importPreviewText, "\n")
		bodyH := max(4, m.h-13)
		maxScroll := max(0, len(lines)-bodyH)
		if m.importPreviewScroll < maxScroll {
			m.importPreviewScroll++
		}
		return m, nil
	}
	if k == "K" {
		if m.importPreviewScroll > 0 {
			m.importPreviewScroll--
		}
		return m, nil
	}
	vis := m.importVisible()
	switch k {
	case "esc", "q":
		m.mode = importProvider
		m.importVisual = false
	case "j", "down":
		if m.importCursor < len(vis)-1 {
			m.importCursor++
		}
	case "k", "up":
		if m.importCursor > 0 {
			m.importCursor--
		}
	case "g":
		m.importCursor = 0
	case "G":
		m.importCursor = max(0, len(vis)-1)
	case " ":
		if len(vis) > 0 {
			id := vis[m.importCursor].ID
			m.importSelected[id] = !m.importSelected[id]
		}
	case "v":
		if m.importVisual {
			m.selectImportRange(vis)
			m.importVisual = false
		} else {
			m.importVisual = true
			m.importVisualStart = m.importCursor
		}
	case "a":
		for _, r := range vis {
			m.importSelected[r.ID] = true
		}
	case "A":
		m.importSelected = map[string]bool{}
	case "f":
		switch m.importFilter {
		case "active":
			m.importFilter = "available"
		case "available":
			m.importFilter = "all"
		case "all":
			m.importFilter = "unavailable"
		default:
			m.importFilter = "active"
		}
		m.importCursor = 0
	case "enter", "i":
		if m.importRunning {
			m.msg = "import already running"
			return m, nil
		}
		if len(m.importIDs()) > 0 {
			m.pendingImportTest = false
			m.mode = confirmImport
		} else {
			m.msg = "no import selection"
		}
	}
	return m, nil
}

func (m Model) openImportProvider() (tea.Model, tea.Cmd) {
	m.importReturn = m.mode
	m.importProviders = importProviderOptions()
	m.importProviderCur = 0
	m.mode = importProvider
	m.importSelected = map[string]bool{}
	m.importVisual = false
	return m, nil
}

func (m Model) updateImportProvider(k string) (tea.Model, tea.Cmd) {
	if len(m.importProviders) == 0 {
		m.importProviders = importProviderOptions()
	}
	switch k {
	case "esc", "q":
		if m.importReturn == importProvider || m.importReturn == confirmImport {
			m.importReturn = normal
		}
		m.mode = m.importReturn
		return m, nil
	case "j", "down":
		if m.importProviderCur < len(m.importProviders)-1 {
			m.importProviderCur++
		}
	case "k", "up":
		if m.importProviderCur > 0 {
			m.importProviderCur--
		}
	case "g":
		m.importProviderCur = 0
	case "G":
		m.importProviderCur = max(0, len(m.importProviders)-1)
	case "enter", "i":
		if len(m.importProviders) == 0 {
			m.msg = "no import providers"
			return m, nil
		}
		p := m.importProviders[m.importProviderCur]
		if !p.Ready {
			m.msg = p.Label + " import unavailable"
			return m, nil
		}
		m.importProvider = p.ID
		return m.openImportFilePicker()
	}
	return m, nil
}

func importProviderOptions() []importProviderOption {
	return []importProviderOption{
		{ID: "kiro", Label: "Kiro", Description: "official Kiro import via API", Source: "", Ready: true},
		{ID: "codex", Label: "OpenAI Codex", Description: "dev DB direct import", Source: "", Ready: true},
		{ID: "antigravity", Label: "Anti Gravity", Description: "dev DB direct import", Source: "", Ready: true},
	}
}

func importProviderLabel(id string) string {
	switch id {
	case "kiro", "":
		return "Kiro"
	case "codex":
		return "OpenAI Codex"
	case "antigravity":
		return "Anti Gravity"
	default:
		return id
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func (m Model) importVisible() []importer.KiroAccount {
	rows := append([]importer.KiroAccount(nil), m.importRows...)
	out := rows[:0]
	for _, r := range rows {
		if m.importFilter == "active" && strings.ToLower(r.Status) != "active" {
			continue
		}
		if m.importFilter == "available" && !r.Available {
			continue
		}
		if m.importFilter == "unavailable" && r.Available {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if m.importSort == "status" && out[i].Status != out[j].Status {
			return out[i].Status < out[j].Status
		}
		return out[i].Email < out[j].Email
	})
	return out
}
func (m Model) selectImportRange(vis []importer.KiroAccount) {
	a, b := m.importVisualStart, m.importCursor
	if a > b {
		a, b = b, a
	}
	for i := a; i <= b && i < len(vis); i++ {
		m.importSelected[vis[i].ID] = true
	}
}
func (m Model) importIDs() []string {
	var ids []string
	for id, on := range m.importSelected {
		if on {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// mainBodyH returns the row count for the scrollable rows region inside the
// rightView pane. Total layout = topbar(4) + pane(bodyH+5) + footer(3) = bodyH+12.
// Pane chrome inside the box: 2 border + 1 sectionTitle + 2 tableHead-with-border = 5.
// When the import status bar is shown, it adds 4 more rows above the layout.
// mainBoxH returns the total rendered height the main pane must occupy
// (including its rounded border). The pane fills exactly the space left
// after the header, footer, and optional import status bar.
func (m Model) mainBoxH() int {
	headerH := lipgloss.Height(m.headerView(0))
	footerH := lipgloss.Height(m.footerView())
	barH := 0
	if bar := m.importStatusBar(); bar != "" {
		barH = lipgloss.Height(bar)
	}
	return max(6, m.h-headerH-footerH-barH)
}

// mainBodyH returns how many account rows fit inside the pane body,
// after subtracting border (2), section title (1), and table head + its
// bottom border (2).
func (m Model) mainBodyH() int {
	return max(2, m.mainBoxH()-5)
}

func (m Model) leftView(w int) string {
	var b strings.Builder
	counts := map[string]int{"all": len(m.accounts)}
	for _, a := range m.accounts {
		counts[a.Provider]++
	}
	b.WriteString(sectionTitle.Render("PROVIDERS") + "\n")
	wroteMediaHeader := false
	for i, p := range m.providers {
		if p != "all" && isMediaProviderName(p) && !wroteMediaHeader {
			b.WriteString("\n" + sectionTitle.Render("MEDIA PROVIDERS") + "\n")
			wroteMediaHeader = true
		}
		cur := "  "
		if i == m.group {
			cur = "▸ "
		}
		name := trim(providerDisplayName(p), max(8, w-12))
		line := fmt.Sprintf("%s%-*s %4d", cur, max(8, w-12), name, counts[p])
		if i == m.group {
			line = providerChip(p).Render(line)
		} else {
			line = subtle.Render(line)
		}
		b.WriteString(line + "\n")
	}
	border := pane
	if m.focusLeft {
		border = focusPane
	}
	// Width(w) includes the border. Height(N) sets the OUTER height including border.
	// Subtract 2 for the rounded border so the inner content area equals m.mainBoxH()-2.
	innerW := max(2, w-4) // 2 border + 2 padding
	return border.Width(w).Height(m.mainBoxH() - 2).Render(strings.TrimRight(clipBlock(b.String(), innerW), "\n"))
}

func (m Model) rightView(w int) string {
	vis := m.visible()
	var b strings.Builder
	innerW := max(2, w-4) // border + horizontal padding
	b.WriteString(sectionTitle.Render("ACCOUNTS") + muted.Render(fmt.Sprintf("  %d rows", len(vis))) + "\n")
	// Column math must use pane inner width, not outer width. Leave left/right
	// breathing room so STATE never spills into clipped edge.
	tableW := max(40, innerW-2)
	prefixW := 4 // cursor + checkbox + spaces
	stateW := 8
	prioW := 4
	updatedW := 10
	providerW := clamp(tableW/5, 10, 16)
	nameW := max(12, tableW-prefixW-providerW-stateW-prioW-updatedW-10)

	// Scroll window: keep cursor visible, don't overflow box
	bodyH := m.mainBodyH() // rows region inside the box (excluding section title + table head)
	maxScroll := max(0, len(vis)-bodyH)
	cursor := m.cursor
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(vis)-1 {
		cursor = max(0, len(vis)-1)
	}
	// Center-ish scroll: keep cursor inside [start, start+bodyH)
	start := 0
	if len(vis) > bodyH {
		start = cursor - bodyH/2
		if start < 0 {
			start = 0
		}
		if start > maxScroll {
			start = maxScroll
		}
	}
	end := min(len(vis), start+bodyH)

	header := padANSI("", prefixW) + padANSI(m.mainHeadLabel("name/email", "name"), nameW) + "  " + padANSI(m.mainHeadLabel("provider", "provider"), providerW) + "  " + padANSI(m.mainHeadLabel("state", "state"), stateW) + "  " + padANSI(m.mainHeadLabel("prio", "priority"), prioW) + "  " + padANSI(m.mainHeadLabel("updated", "updated"), updatedW)
	if len(vis) > bodyH {
		header += muted.Render(fmt.Sprintf("   %d-%d/%d", start+1, end, len(vis)))
	}
	b.WriteString(tableHead.Render(header) + "\n")

	for i := start; i < end; i++ {
		a := vis[i]
		chk := "○"
		if m.selected[a.ID] {
			chk = "●"
		}
		mark := " "
		if i == m.cursor {
			mark = "▸"
		}
		label := a.Name
		if label == "" {
			label = a.Email
		}
		state := inactive.Render("inactive")
		if a.IsActive {
			state = ok.Render("active")
		}
		prefix := fmt.Sprintf("%s %s ", mark, chk)
		line := padANSI(prefix, prefixW) + padANSI(trim(label, nameW), nameW) + "  " + padANSI(trim(a.Provider, providerW), providerW) + "  " + padANSI(state, stateW) + "  " + padANSI(fmt.Sprintf("%d", a.Priority), prioW) + "  " + padANSI(shortDate(a.UpdatedAt), updatedW)
		if m.mode == visual {
			lo, hi := m.visualStart, m.cursor
			if lo > hi {
				lo, hi = hi, lo
			}
			if i >= lo && i <= hi {
				line = visStyle.Render(line)
			}
		}
		if !m.focusLeft && i == m.cursor {
			line = sel.Render(line)
		}
		b.WriteString(line + "\n")
	}
	if len(vis) == 0 {
		b.WriteString(empty.Render("\n  no accounts match current group/search\n"))
	}
	border := pane
	if !m.focusLeft {
		border = focusPane
	}
	return border.Width(w).Height(m.mainBoxH() - 2).Render(strings.TrimRight(clipBlock(b.String(), innerW), "\n"))
}

func (m Model) backupView() string {
	outerW := max(60, m.w-2)
	bodyH := max(12, m.h-6)

	leftW := outerW / 2
	rightW := outerW - leftW - 1 // 1 char gap

	leftInnerW := max(20, leftW-4)
	rightInnerW := max(20, rightW-4)
	paneInnerH := max(4, bodyH-2)

	leftPane := renderFixedPane(pane, leftInnerW, paneInnerH, m.backupListContent(leftInnerW, paneInnerH))
	rightPane := renderFixedPane(pane, rightInnerW, paneInnerH, m.backupPreviewContent(rightInnerW, paneInnerH))

	return lipgloss.JoinHorizontal(lipgloss.Top, leftPane, " ", rightPane)
}

func (m Model) backupListContent(innerW, innerH int) string {
	var b strings.Builder

	// Header lines (4): title, summary, hints, table header
	b.WriteString(clipLine(brand.Render(" Recovery "), innerW) + "\n")
	if len(m.undoInfos) > 0 {
		b.WriteString(clipLine(muted.Render(fmt.Sprintf(" undo %d/%d", m.undoCursor+1, len(m.undoInfos))), innerW) + "\n")
	} else {
		b.WriteString(clipLine(muted.Render(" no undo logs"), innerW) + "\n")
	}
	b.WriteString(clipLine(muted.Render(" R restore  C clean+VACUUM  F snapshot  Esc back"), innerW) + "\n")

	nameW := max(10, innerW-18)
	b.WriteString(importHead.Render(clipLine(fmt.Sprintf(" %-*s %5s %-7s", nameW, "UNDO LOG", "ACCTS", "SIZE"), innerW)) + "\n")

	headerLines := 4
	listH := max(1, innerH-headerLines)

	if len(m.undoInfos) == 0 {
		b.WriteString(clipLine(empty.Render("  no undo logs yet"), innerW) + "\n")
		return b.String()
	}

	maxScroll := max(0, len(m.undoInfos)-listH)
	start := clamp(m.backupScroll, 0, maxScroll)
	if m.undoCursor < start {
		start = m.undoCursor
	} else if m.undoCursor >= start+listH {
		start = m.undoCursor - listH + 1
	}
	start = clamp(start, 0, maxScroll)
	end := min(len(m.undoInfos), start+listH)

	for i := start; i < end; i++ {
		x := m.undoInfos[i]
		cur := "  "
		if i == m.undoCursor {
			cur = "▸ "
		}
		line := fmt.Sprintf("%s%-*s %5d %-7s", cur, nameW, trim(shortName(x.Path), nameW), x.Accounts, humanSize(x.Size))
		line = clipLine(line, innerW)
		if i == m.undoCursor {
			line = sel.Render(line)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func (m Model) backupPreviewContent(innerW, innerH int) string {
	var b strings.Builder

	// Header lines (3): title, meta, separator
	b.WriteString(clipLine(brand.Render(" Preview "), innerW) + "\n")
	if len(m.undoInfos) > 0 && m.undoCursor < len(m.undoInfos) {
		x := m.undoInfos[m.undoCursor]
		prov := providerLineCompact(x.ProviderCount, max(8, innerW-20))
		header := fmt.Sprintf("%d Accounts", x.Accounts)
		if prov != "" {
			header += " | " + prov
		}
		b.WriteString(clipLine(subtle.Render(header), innerW) + "\n")
	} else {
		b.WriteString(clipLine(subtle.Render("(no log selected)"), innerW) + "\n")
	}
	b.WriteString(clipLine(strings.Repeat("─", max(1, innerW)), innerW) + "\n")

	headerLines := 3
	footerLines := 1
	contentH := max(1, innerH-headerLines-footerLines)

	lines := strings.Split(m.restorePreviewText, "\n")
	if m.restorePreviewText == "" {
		lines = []string{"(no file content)"}
	}
	maxScroll := max(0, len(lines)-contentH)
	start := clamp(m.restorePreviewScroll, 0, maxScroll)
	end := min(len(lines), start+contentH)

	for _, line := range lines[start:end] {
		b.WriteString(clipLine(line, innerW) + "\n")
	}
	for i := end - start; i < contentH; i++ {
		b.WriteString("\n")
	}
	b.WriteString(clipLine(muted.Render(fmt.Sprintf(" Shift+J/K scroll  %d-%d/%d", start+1, end, len(lines))), innerW))
	return b.String()
}

func (m Model) inspectView() string {
	lines := strings.Split(m.inspectText, "\n")
	outerW := clamp(m.w-4, 76, max(76, m.w-4))
	innerW := max(40, outerW-6)
	bodyH := max(6, m.h-9)
	maxScroll := max(0, len(lines)-bodyH)
	start := clamp(m.inspectScroll, 0, maxScroll)
	end := min(len(lines), start+bodyH)
	var b strings.Builder
	b.WriteString(brand.Render(fmt.Sprintf(" Inspect JSON  %d account(s) ", len(m.inspectIDs))) + muted.Render(fmt.Sprintf("  lines %d-%d/%d", start+1, end, len(lines))) + "\n")
	b.WriteString(muted.Render(" long lines clipped; press e to export full JSON to ./.accounts/") + "\n\n")
	for _, line := range lines[start:end] {
		b.WriteString(clipLine(line, innerW) + "\n")
	}
	for i := end - start; i < bodyH; i++ {
		b.WriteString("\n")
	}
	b.WriteString("\n" + muted.Render("  j/k or arrows scroll   gg/G top/bottom   e export JSON   Esc back   Ctrl+Q quit"))
	return box.Width(outerW).Render(b.String())
}

func (m Model) importProviderView() string {
	ps := m.importProviders
	if len(ps) == 0 {
		ps = importProviderOptions()
	}
	var b strings.Builder
	b.WriteString(brand.Render(" Import Provider ") + "\n")
	b.WriteString(muted.Render(" choose provider before loading import rows") + "\n\n")
	b.WriteString(importHead.Render(fmt.Sprintf("%-3s %-16s %-44s %s", "", "PROVIDER", "MODE", "SOURCE")) + "\n")
	for i, p := range ps {
		mark := " "
		if i == m.importProviderCur {
			mark = "▸"
		}
		state := ok.Render("ready")
		if !p.Ready {
			state = inactive.Render("disabled")
		}
		line := fmt.Sprintf("%s  %-16s %-44s %-50s %s", mark, trim(p.Label, 16), trim(p.Description, 44), trim(p.Source, 50), state)
		if !p.Ready {
			line = muted.Render(line)
		}
		if i == m.importProviderCur {
			line = sel.Render(line)
		}
		b.WriteString(line + "\n")
	}
	if len(ps) == 0 {
		b.WriteString(empty.Render("\n  no import providers configured\n"))
	}
	b.WriteString("\n" + muted.Render(" j/k move  gg/G jump  Enter/i choose  Esc back  Ctrl+Q quit"))
	return centered(m.w, box.Width(max(104, m.w-6)).Render(b.String()))
}

func (m Model) importView() string {
	outerW := max(60, m.w-2)
	bodyH := max(12, m.h-6)

	leftW := outerW / 2
	rightW := outerW - leftW - 1

	leftInnerW := max(20, leftW-4)
	rightInnerW := max(20, rightW-4)
	paneInnerH := max(4, bodyH-2)

	leftPane := renderFixedPane(focusPane, leftInnerW, paneInnerH, m.importLeftContent(leftInnerW, paneInnerH))
	rightPane := renderFixedPane(pane, rightInnerW, paneInnerH, m.importRightContent(rightInnerW, paneInnerH))

	return lipgloss.JoinHorizontal(lipgloss.Top, leftPane, " ", rightPane)
}

func (m Model) importLeftContent(innerW, innerH int) string {
	vis := m.importVisible()
	avail := 0
	for _, r := range m.importRows {
		if r.Available {
			avail++
		}
	}

	var b strings.Builder
	// Header lines (4): title, summary, hints, table head
	b.WriteString(clipLine(brand.Render(" "+importProviderLabel(m.importProvider)+" Import "), innerW) + "\n")
	b.WriteString(clipLine(muted.Render(fmt.Sprintf(" rows:%d vis:%d avail:%d sel:%d filter:%s", len(m.importRows), len(vis), avail, len(m.importIDs()), m.importFilter)), innerW) + "\n")
	b.WriteString(clipLine(muted.Render(" Space sel  v visual  a/A all/clear  f filter  Enter import  Esc back"), innerW) + "\n")

	statusW := 8
	reasonW := max(8, innerW/4)
	emailW := max(10, innerW-6-statusW-reasonW-3)
	b.WriteString(importHead.Render(clipLine(fmt.Sprintf(" %2s %s %-*s %-*s %s", "#", "S", emailW, "EMAIL", statusW, "STATUS", "REASON"), innerW)) + "\n")

	headerLines := 4
	maxRows := max(1, innerH-headerLines)

	if len(vis) == 0 {
		b.WriteString(clipLine(empty.Render("  no import rows match filter"), innerW))
		return b.String()
	}

	start := clamp(m.importCursor-maxRows/2, 0, max(0, len(vis)-maxRows))
	end := min(len(vis), start+maxRows)
	for i := start; i < end; i++ {
		r := vis[i]
		mark := " "
		if i == m.importCursor {
			mark = "▸"
		}
		chk := "○"
		if m.importSelected[r.ID] {
			chk = "●"
		}
		reason := r.Reason
		if !r.Available && reason == "" {
			reason = "unavailable"
		}
		line := fmt.Sprintf("%s%2d %s %-*s %-*s %-*s", mark, i+1, chk, emailW, trim(r.Email, emailW), statusW, trim(r.Status, statusW), reasonW, trim(reason, reasonW))
		line = clipLine(line, innerW)
		if !r.Available {
			line = muted.Render(line)
		}
		if m.importVisual {
			lo, hi := m.importVisualStart, m.importCursor
			if lo > hi {
				lo, hi = hi, lo
			}
			if i >= lo && i <= hi {
				line = visStyle.Render(line)
			}
		}
		if i == m.importCursor {
			line = sel.Render(line)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func (m Model) importRightContent(innerW, innerH int) string {
	var b strings.Builder

	// Header lines (3): title, meta/path, separator
	b.WriteString(clipLine(brand.Render(" File Content "), innerW) + "\n")
	if m.importAccountsPath != "" {
		b.WriteString(clipLine(muted.Render(" "+shortName(m.importAccountsPath)), innerW) + "\n")
	} else {
		b.WriteString(clipLine(muted.Render(" (no file)"), innerW) + "\n")
	}
	b.WriteString(clipLine(strings.Repeat("─", max(1, innerW)), innerW) + "\n")

	headerLines := 3
	footerLines := 1
	contentH := max(1, innerH-headerLines-footerLines)

	if m.importPreviewText == "" {
		b.WriteString(clipLine(empty.Render("  (file content not loaded)"), innerW) + "\n")
		for i := 1; i < contentH; i++ {
			b.WriteString("\n")
		}
		b.WriteString(clipLine(muted.Render(" Shift+J/K scroll"), innerW))
		return b.String()
	}

	lines := strings.Split(m.importPreviewText, "\n")
	maxScroll := max(0, len(lines)-contentH)
	start := clamp(m.importPreviewScroll, 0, maxScroll)
	end := min(len(lines), start+contentH)
	for _, line := range lines[start:end] {
		b.WriteString(clipLine(line, innerW) + "\n")
	}
	for i := end - start; i < contentH; i++ {
		b.WriteString("\n")
	}
	b.WriteString(clipLine(muted.Render(fmt.Sprintf(" Shift+J/K scroll  %d-%d/%d", start+1, end, len(lines))), innerW))
	return b.String()
}

func (m Model) confirmImportView() string {
	ids := m.importIDs()
	n := len(ids)
	verb := "Import Accounts"
	action := "INSERT/UPDATE selected rows into the database."
	if m.pendingImportTest {
		verb = "Test Import Accounts"
		action = "Import up to 5 selected rows as a test."
	}
	provider := importProviderLabel(m.importProvider)
	if provider == "" {
		provider = m.importProvider
	}
	file := shortName(m.importAccountsPath)
	if file == "" {
		file = "(no file)"
	}

	var b strings.Builder
	b.WriteString(warnChip.Render(" ⚠ "+verb+" ") + "\n\n")
	b.WriteString(fmt.Sprintf("Provider: %s\n", provider))
	b.WriteString(fmt.Sprintf("File: %s\n", file))
	b.WriteString(fmt.Sprintf("Accounts: %d\n\n", n))
	b.WriteString(action + "\n\n")
	b.WriteString(warnChip.Render(" Y/Enter ") + "  confirm    " + muted.Render("N/Esc") + "  cancel")
	return restoreBox.Render(b.String())
}

func (m Model) confirmDeleteView() string {
	ids := m.ids()
	n := len(ids)
	// Count per provider
	provCounts := map[string]int{}
	for _, a := range m.accounts {
		if m.selected[a.ID] {
			p := a.Provider
			if p == "" {
				p = "unknown"
			}
			provCounts[p]++
		}
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Delete %d selected accounts?\n\n", n))
	for prov, cnt := range provCounts {
		label := prov
		switch prov {
		case "codex":
			label = "OpenAI Codex"
		case "antigravity":
			label = "Anti Gravity"
		case "kiro":
			label = "Kiro"
		}
		b.WriteString(fmt.Sprintf("  %d - %s\n", cnt, label))
	}
	b.WriteString("\n")
	b.WriteString(warnChip.Render(" Y/Enter ") + "  confirm    " + muted.Render("N/Esc") + "  cancel")
	return dangerBox.Render(b.String())
}

func is9RouterRunning() bool {
	cmd := exec.Command("pgrep", "-f", "9router")
	if runtime.GOOS == "windows" {
		cmd = exec.Command("tasklist", "/FI", "IMAGENAME eq 9router.exe")
	}
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(string(out)))
	return strings.Contains(text, "9router")
}

func kill9RouterProcess() {
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/F", "/IM", "9router.exe").Run()
		return
	}
	_ = exec.Command("pkill", "-f", "9router").Run()
}

func (m Model) vacuumConfirmView() string {
	var b strings.Builder
	if m.vacuum9RRunning {
		b.WriteString(warnChip.Render(" ⚠ 9Router is running ") + "\n\n")
		b.WriteString("VACUUM requires exclusive DB lock.\n")
		b.WriteString("Will kill running 9router process → backup → VACUUM. Restart 9router manually after.\n\n")
	} else {
		b.WriteString(okChip.Render(" 9Router not running ") + "\n\n")
		b.WriteString("Clean request/usage logs + VACUUM database.\n\n")
	}
	b.WriteString("Proceed?\n\n")
	b.WriteString(warnChip.Render(" Y/Enter ") + "  confirm    " + muted.Render("N/Esc") + "  cancel")
	return vacuumBox.Render(b.String())
}

func (m Model) restoreConfirmView() string {
	var b strings.Builder
	info := m.undoInfos[m.undoCursor]
	b.WriteString(warnChip.Render(" ⚠ Restore Accounts ") + "\n\n")
	b.WriteString(fmt.Sprintf("File: %s\n", filepath.Base(info.Path)))
	b.WriteString(fmt.Sprintf("Accounts: %d\n", info.Accounts))
	b.WriteString(fmt.Sprintf("Size: %s\n\n", humanSize(info.Size)))
	b.WriteString("This will INSERT rows back into the database.\n\n")
	b.WriteString(warnChip.Render(" Y/Enter ") + "  confirm    " + muted.Render("N/Esc") + "  cancel")
	return restoreBox.Render(b.String())
}

func (m Model) vacuumCmd() tea.Cmd {
	return func() tea.Msg {
		need9R := m.vacuum9RRunning
		if need9R {
			kill9RouterProcess()
			time.Sleep(2 * time.Second)
		}
		s, err := m.r.CleanVacuum()
		if need9R {
			s += "; 9router was stopped, rerun 9router manually"
		}
		if err != nil {
			return vacuumDoneMsg{err: err}
		}
		return vacuumDoneMsg{result: s}
	}
}
func summaryRow(x domain.BackupInfo, active bool, label string) string {
	cur := "  "
	if active {
		cur = "> "
	}
	return fmt.Sprintf("%s%-7s accounts:%-4d size:%-10s modified:%s\n", cur, label, x.Accounts, humanSize(x.Size), shortTime(x.Modified))
}
func diffFromCurrent(cur, x domain.BackupInfo) string {
	var b strings.Builder
	delta := x.Accounts - cur.Accounts
	if delta != 0 {
		b.WriteString(fmt.Sprintf("    accounts diff: %+d\n", delta))
	}
	keys := map[string]bool{}
	for k := range cur.ProviderCount {
		keys[k] = true
	}
	for k := range x.ProviderCount {
		keys[k] = true
	}
	var list []string
	for k := range keys {
		list = append(list, k)
	}
	sort.Strings(list)
	wrote := false
	for _, k := range list {
		d := x.ProviderCount[k] - cur.ProviderCount[k]
		if d != 0 {
			if !wrote {
				b.WriteString("    provider diff:\n")
				wrote = true
			}
			b.WriteString(fmt.Sprintf("      %-26s %+d  (%d -> %d)\n", trim(k, 26), d, cur.ProviderCount[k], x.ProviderCount[k]))
		}
	}
	if !wrote && delta == 0 {
		b.WriteString("    same accounts/provider counts\n")
	}
	return b.String()
}
func humanSize(n int64) string {
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
func shortTime(s string) string {
	if len(s) >= 16 {
		return s[:16]
	}
	return s
}
func shortDate(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}
func shortName(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return p
}
func providerLine(m map[string]int) string {
	return providerLineCompact(m, 120) + "\n"
}

func providerLineCompact(m map[string]int, width int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", trim(k, 10), m[k]))
	}
	if len(parts) == 0 {
		return ""
	}
	return clipLine(strings.Join(parts, " "), max(8, width))
}
func helpView() string {
	return box.Render(brand.Render(" Hotkeys ") + "\n\n" + "j/k        move\nh/l        focus pane\ngg/G       top/bottom\nEnter/i    inspect account JSON\n  e        export inspected JSON to ./.accounts/\nSpace      toggle account\nv          visual select range\na/A        select all visible / clear\nd          delete selected (writes tiny ./.tui-logs undo log)\nb          recovery/maintenance\n  R        restore selected undo log\n  C        clean request/usage logs + VACUUM\n  F        full SQLite snapshot (.bak/.bak2)\nr          refresh\nq          quit\nEsc        cancel")
}
func labelOf(a domain.Account) string {
	if a.Name != "" {
		return a.Name
	}
	return a.Email
}

func compareAccounts(a, b domain.Account, field string) int {
	cmpStr := func(x, y string) int { return strings.Compare(strings.ToLower(x), strings.ToLower(y)) }
	switch field {
	case "provider":
		if c := cmpStr(a.Provider, b.Provider); c != 0 {
			return c
		}
	case "priority":
		if a.Priority != b.Priority {
			return a.Priority - b.Priority
		}
	case "updated":
		if c := cmpStr(a.UpdatedAt, b.UpdatedAt); c != 0 {
			return c
		}
	case "state":
		av, bv := 0, 0
		if a.IsActive {
			av = 1
		}
		if b.IsActive {
			bv = 1
		}
		if av != bv {
			return av - bv // inactive first in asc
		}
	}
	if c := cmpStr(labelOf(a), labelOf(b)); c != 0 {
		return c
	}
	return cmpStr(a.ID, b.ID)
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
func clipLine(s string, n int) string {
	if n <= 1 {
		return ""
	}
	// ANSI-aware truncation. Rune slicing can cut escape reset codes from
	// styled text (importHead/sel/etc.), causing background color bleed into
	// following lines.
	if ansi.StringWidth(s) <= n {
		return s
	}
	return ansi.Truncate(s, n, "")
}

func padANSI(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "")
	if ansi.StringWidth(s) < w {
		s += strings.Repeat(" ", w-ansi.StringWidth(s))
	}
	return s
}

func importHeadFull(s string, w int) string {
	if w <= 0 {
		return ""
	}
	// Full-width header bar. No padding here: caller's column math owns
	// alignment. Width(w) makes background fill to pane edge.
	s = ansi.Truncate(s, w, "")
	return importHead.Copy().Padding(0, 0).Width(w).MaxWidth(w).Render(s)
}

// clipBlock clips every line of s to width n.
func clipBlock(s string, n int) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = clipLine(ln, n)
	}
	return strings.Join(lines, "\n")
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func clamp(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
func centered(w int, s string) string {
	return lipgloss.PlaceHorizontal(max(w, lipgloss.Width(s)), lipgloss.Center, s)
}

// padToHeight pads (or trims) content to exactly h lines so that
// lipgloss.JoinVertical/JoinHorizontal align panes consistently.
func padToHeight(content string, h int) string {
	return fitBlock(content, 0, h)
}

// fitBlock clips content to at most h rendered rows and optional width w,
// then pads to exactly h rows. Use this before lipgloss pane rendering:
// Height() is a minimum, not a maximum, so overflowing content grows panes
// and breaks split-pane bottom alignment.
func fitBlock(content string, w, h int) string {
	if h <= 0 {
		return ""
	}
	content = strings.TrimRight(content, "\n")
	if content == "" {
		content = ""
	}
	out := make([]string, 0, h)
	for _, ln := range strings.Split(content, "\n") {
		if len(out) >= h {
			break
		}
		if w > 0 {
			ln = clipLine(ln, w)
		}
		// Styled lines should normally be one row. If any style/content wraps
		// visually, keep first rendered row only so pane height stays fixed.
		parts := strings.Split(strings.TrimRight(ln, "\n"), "\n")
		for _, p := range parts {
			if len(out) >= h {
				break
			}
			if w > 0 {
				p = clipLine(p, w)
			}
			out = append(out, p)
		}
	}
	for len(out) < h {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

func renderFixedPane(st lipgloss.Style, innerW, innerH int, content string) string {
	innerW = max(1, innerW)
	innerH = max(1, innerH)
	// Lipgloss Height is a minimum. If styled content still wraps/overflows,
	// rendered pane grows. Force exact outer height after rendering.
	rendered := st.Width(innerW).Height(innerH).Render(fitBlock(content, innerW, innerH))
	outerH := innerH + 2 // rounded border top+bottom
	lines := strings.Split(rendered, "\n")
	if len(lines) > outerH {
		// Preserve bottom border; crop overflowing middle content instead.
		cropped := append([]string{}, lines[:outerH-1]...)
		cropped = append(cropped, lines[len(lines)-1])
		lines = cropped
	}
	for len(lines) < outerH {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// overlayPopup renders popup centered over bg. Background content outside
// the popup rectangle stays visible. Inside the popup rectangle, background
// characters are erased and popup content is drawn.
func overlayPopup(w, h int, bg, popup string) string {
	bgLines := strings.Split(bg, "\n")
	popupLines := strings.Split(popup, "\n")

	popupH := len(popupLines)
	popupW := lipgloss.Width(popup)

	// Pad bg to fill terminal height
	for len(bgLines) < h {
		bgLines = append(bgLines, strings.Repeat(" ", w))
	}

	// Calculate centered position
	startY := (h - popupH) / 2
	startX := (w - popupW) / 2
	if startY < 0 {
		startY = 0
	}
	if startX < 0 {
		startX = 0
	}

	// For each popup line, splice it into the background line
	for i, pLine := range popupLines {
		row := startY + i
		if row >= len(bgLines) {
			break
		}

		bgLine := bgLines[row]

		// ANSI-aware: keep left portion of bg (0..startX)
		left := ansi.Truncate(bgLine, startX, "")
		// Pad left to exact startX width in case bg line is shorter
		leftW := ansi.StringWidth(left)
		if leftW < startX {
			left += strings.Repeat(" ", startX-leftW)
		}

		// ANSI-aware: keep right portion of bg (after startX+popupW)
		right := ansi.TruncateLeft(bgLine, startX+popupW, "")

		// Pad popup line to popupW so right side aligns
		pLineW := ansi.StringWidth(pLine)
		paddedPopup := pLine
		if pLineW < popupW {
			paddedPopup += strings.Repeat(" ", popupW-pLineW)
		}

		bgLines[row] = left + paddedPopup + right
	}

	return strings.Join(bgLines[:h], "\n")
}

func (m Model) openImportFilePicker() (tea.Model, tea.Cmd) {
	accountsPath := strings.TrimSpace(os.Getenv("NINETUI_ACCOUNTS_PATH"))
	if accountsPath == "" {
		accountsPath = filepath.Join(filepath.Dir(os.Args[0]), ".accounts") + string(os.PathSeparator)
	}

	// If path ends with /, treat as directory → show file picker
	if strings.HasSuffix(accountsPath, string(os.PathSeparator)) || strings.HasSuffix(accountsPath, "/") {
		accountsDir := strings.TrimRight(accountsPath, string(os.PathSeparator)+"/")
		_ = os.MkdirAll(accountsDir, 0755)
		files, err := os.ReadDir(accountsDir)
		if err != nil {
			m.msg = "failed to read accounts directory: " + err.Error()
			m.mode = importProvider
			return m, nil
		}

		var validFiles []string
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			name := f.Name()
			if strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".txt") {
				validFiles = append(validFiles, name)
			}
		}

		if len(validFiles) == 0 {
			m.msg = "no .json or .txt files found in " + accountsDir
			m.mode = importProvider
			return m, nil
		}

		sort.Strings(validFiles)
		m.importFiles = validFiles
		m.importFileCursor = 0
		m.mode = importFilePicker
		m.pickerFocus = 0
		m.pickerAccCursor = 0
		m.importPreviewPath = ""
		m.importPreviewAccounts = nil
		m.importPreviewText = ""
		m.importPreviewScroll = 0
		// fire initial preview load for first file
		if len(validFiles) > 0 {
			fullPath := filepath.Join(accountsDir, validFiles[0])
			return m, m.loadFilePreviewCmd(fullPath)
		}
		return m, nil
	}

	// Path is a file → skip file picker, load directly
	m.importAccountsPath = accountsPath
	return m.openImportScreen()
}

func (m Model) updateImportFilePicker(k string) (tea.Model, tea.Cmd) {
	// J/K scroll right-bottom file content from anywhere
	if k == "J" {
		lines := strings.Split(m.importPreviewText, "\n")
		bodyH := max(4, m.h-15)
		maxScroll := max(0, len(lines)-bodyH)
		if m.importPreviewScroll < maxScroll {
			m.importPreviewScroll++
		}
		return m, nil
	}
	if k == "K" {
		if m.importPreviewScroll > 0 {
			m.importPreviewScroll--
		}
		return m, nil
	}

	switch k {
	case "esc", "q":
		m.mode = importProvider
		m.pickerFocus = 0
		return m, nil
	case "l", "right":
		if m.pickerFocus == 0 && len(m.importPreviewAccounts) > 0 {
			m.pickerFocus = 1
			m.pickerAccCursor = 0
		}
		return m, nil
	case "h", "left":
		if m.pickerFocus == 1 {
			m.pickerFocus = 0
		}
		return m, nil
	}

	if m.pickerFocus == 1 {
		// keys operate on right-top accounts preview
		switch k {
		case "j", "down":
			if m.pickerAccCursor < len(m.importPreviewAccounts)-1 {
				m.pickerAccCursor++
			}
		case "k", "up":
			if m.pickerAccCursor > 0 {
				m.pickerAccCursor--
			}
		case "g":
			m.pickerAccCursor = 0
		case "G":
			m.pickerAccCursor = max(0, len(m.importPreviewAccounts)-1)
		}
		return m, nil
	}

	// pickerFocus == 0: file list
	prevCursor := m.importFileCursor
	switch k {
	case "j", "down":
		if m.importFileCursor < len(m.importFiles)-1 {
			m.importFileCursor++
		}
	case "k", "up":
		if m.importFileCursor > 0 {
			m.importFileCursor--
		}
	case "g":
		m.importFileCursor = 0
	case "G":
		m.importFileCursor = max(0, len(m.importFiles)-1)
	case "enter", "i":
		if len(m.importFiles) == 0 {
			m.msg = "no files available"
			return m, nil
		}
		selectedFile := m.importFiles[m.importFileCursor]
		accountsPath := strings.TrimSpace(os.Getenv("NINETUI_ACCOUNTS_PATH"))
		if accountsPath == "" {
			accountsPath = filepath.Join(filepath.Dir(os.Args[0]), ".accounts") + string(os.PathSeparator)
		}
		dir := strings.TrimRight(accountsPath, string(os.PathSeparator)+"/")
		fullPath := filepath.Join(dir, selectedFile)
		return m.openImportScreenWithFile(fullPath)
	}

	// trigger preview reload if cursor changed or first view
	if len(m.importFiles) > 0 {
		accountsPath := strings.TrimSpace(os.Getenv("NINETUI_ACCOUNTS_PATH"))
		if accountsPath == "" {
			accountsPath = filepath.Join(filepath.Dir(os.Args[0]), ".accounts") + string(os.PathSeparator)
		}
		dir := strings.TrimRight(accountsPath, string(os.PathSeparator)+"/")
		fullPath := filepath.Join(dir, m.importFiles[m.importFileCursor])
		if m.importFileCursor != prevCursor || m.importPreviewPath != fullPath {
			return m, m.loadFilePreviewCmd(fullPath)
		}
	}
	return m, nil
}

func (m Model) openImportScreenWithFile(accountsPath string) (tea.Model, tea.Cmd) {
	m.importAccountsPath = accountsPath
	m.importSelected = map[string]bool{}
	m.importVisual = false
	m.importCursor = 0
	m.importRows = nil
	m.mode = importScreen
	m.msg = "loading " + filepath.Base(accountsPath) + "..."
	provider := m.importProvider
	dbPath := m.r.Path
	loadRows := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		res, err := importer.RunProvider(ctx, importer.ImportOptions{AccountsPath: accountsPath, DBPath: dbPath, DryRun: true, ActiveOnly: false, IncludeInactive: true}, provider)
		if err != nil {
			return importRowsMsg{err: err}
		}
		return importRowsMsg{rows: res.Rows}
	}
	// Also load file content preview (reuse importPreview* fields)
	return m, tea.Batch(loadRows, m.loadFilePreviewCmd(accountsPath))
}

func (m Model) importFilePickerView() string {
	outerW := max(60, m.w-2)
	bodyH := max(12, m.h-6)

	leftW := outerW / 2
	rightW := outerW - leftW - 1

	leftInnerW := max(20, leftW-4)
	rightInnerW := max(20, rightW-4)
	leftInnerH := max(4, bodyH-2)

	// Left outer height = leftInnerH + 2 (border) = bodyH.
	// Right composite must equal bodyH: topOuterH + botOuterH == bodyH.
	accCount := len(m.importPreviewAccounts)
	topRatio := 0.20
	if accCount >= 10 {
		topRatio = 0.50
	} else if accCount > 0 {
		topRatio = 0.20 + 0.30*float64(accCount)/10.0
	}
	topOuterH := max(5, int(float64(bodyH)*topRatio))
	if topOuterH > bodyH-5 {
		topOuterH = bodyH - 5
	}
	if topOuterH < 5 {
		topOuterH = 5
	}
	botOuterH := bodyH - topOuterH
	topInnerH := max(1, topOuterH-2)
	botInnerH := max(1, botOuterH-2)

	// LEFT pane
	leftStyle := pane
	if m.pickerFocus == 0 {
		leftStyle = focusPane
	}
	leftPane := renderFixedPane(leftStyle, leftInnerW, leftInnerH, m.filePickerListContent(leftInnerW, leftInnerH))

	// RIGHT TOP pane
	topStyle := pane
	if m.pickerFocus == 1 {
		topStyle = focusPane
	}
	topPane := renderFixedPane(topStyle, rightInnerW, topInnerH, m.filePickerAccountsContent(rightInnerW, topInnerH))

	// RIGHT BOTTOM pane
	botPane := renderFixedPane(pane, rightInnerW, botInnerH, m.filePickerJSONContent(rightInnerW, botInnerH))

	rightPane := lipgloss.JoinVertical(lipgloss.Left, topPane, botPane)
	return lipgloss.JoinHorizontal(lipgloss.Top, leftPane, " ", rightPane)
}

func (m Model) filePickerListContent(innerW, innerH int) string {
	var b strings.Builder

	// Header lines (4): title, summary, hints, table head
	b.WriteString(clipLine(brand.Render(" Import File Picker "), innerW) + "\n")
	b.WriteString(clipLine(muted.Render(fmt.Sprintf(" %s  %d files", importProviderLabel(m.importProvider), len(m.importFiles))), innerW) + "\n")
	b.WriteString(clipLine(muted.Render(" Enter pick  j/k move  l/h focus  Shift+J/K scroll"), innerW) + "\n")
	b.WriteString(importHead.Render(clipLine(" FILE", innerW)) + "\n")

	headerLines := 4
	listH := max(1, innerH-headerLines)

	if len(m.importFiles) == 0 {
		b.WriteString(clipLine(empty.Render("  no .json or .txt files"), innerW))
		return b.String()
	}

	st := max(0, m.importFileCursor-listH/2)
	if st > max(0, len(m.importFiles)-listH) {
		st = max(0, len(m.importFiles)-listH)
	}
	en := min(len(m.importFiles), st+listH)
	for i := st; i < en; i++ {
		cur := "  "
		if i == m.importFileCursor {
			cur = "▸ "
		}
		line := fmt.Sprintf("%s%s", cur, m.importFiles[i])
		line = clipLine(line, innerW)
		if i == m.importFileCursor && m.pickerFocus == 0 {
			line = sel.Render(line)
		} else if i == m.importFileCursor {
			line = visStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func (m Model) filePickerAccountsContent(innerW, innerH int) string {
	var b strings.Builder
	b.WriteString(brand.Render(" Accounts Preview ") + "\n")
	b.WriteString(muted.Render(" preview of accounts in selected file ") + "\n\n")

	// One shared column format for header and rows. Keep total width <= innerW:
	// prefix(3) + emailW + gap + provW + gap + statusW.
	prefixW := 3
	emailW := max(12, innerW*45/100)
	provW := max(10, innerW*25/100)
	statusW := max(6, innerW-prefixW-emailW-provW-2)
	if prefixW+emailW+provW+statusW+2 > innerW {
		over := prefixW + emailW + provW + statusW + 2 - innerW
		if emailW-over >= 12 {
			emailW -= over
		} else {
			provW = max(8, provW-over)
		}
		statusW = max(6, innerW-prefixW-emailW-provW-2)
	}

	head := fmt.Sprintf("%-*s%-*s %-*s %-*s", prefixW, "", emailW, "EMAIL", provW, "PROVIDER", statusW, "STATUS")
	b.WriteString(importHeadFull(head, innerW) + "\n")

	if len(m.importPreviewAccounts) == 0 {
		if m.importPreviewPath == "" {
			b.WriteString(empty.Render("  (loading...)"))
		} else {
			b.WriteString(empty.Render("  no accounts in this file"))
		}
		return b.String()
	}

	headerLines := 4
	listH := max(1, innerH-headerLines)

	st := 0
	if m.pickerFocus == 1 {
		st = max(0, m.pickerAccCursor-listH/2)
		if st > max(0, len(m.importPreviewAccounts)-listH) {
			st = max(0, len(m.importPreviewAccounts)-listH)
		}
	}
	en := min(len(m.importPreviewAccounts), st+listH)

	for i := st; i < en; i++ {
		a := m.importPreviewAccounts[i]
		mark := "   "
		if i == m.pickerAccCursor {
			mark = "▸  "
		}
		email := a.Email
		if email == "" {
			email = a.ID
		}
		status := strings.ToLower(a.Status)
		if status == "" {
			status = "—"
		}
		statusRendered := status
		switch status {
		case "active":
			statusRendered = ok.Render(status)
		case "inactive", "unavailable":
			statusRendered = inactive.Render(status)
		}
		line := fmt.Sprintf("%-*s%-*s %-*s %-*s", prefixW, mark, emailW, trim(email, emailW), provW, trim(a.Provider, provW), statusW, trim(statusRendered, statusW))
		line = clipLine(line, innerW)
		if i == m.pickerAccCursor && m.pickerFocus == 1 {
			line = sel.Render(line)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func (m Model) filePickerJSONContent(innerW, innerH int) string {
	var b strings.Builder

	// Header lines (3): title, meta, separator
	b.WriteString(clipLine(brand.Render(" File Content "), innerW) + "\n")
	if m.importPreviewPath != "" {
		b.WriteString(clipLine(muted.Render(" "+shortName(m.importPreviewPath)), innerW) + "\n")
	} else {
		b.WriteString(clipLine(muted.Render(" (no file)"), innerW) + "\n")
	}
	b.WriteString(clipLine(strings.Repeat("─", max(1, innerW)), innerW) + "\n")

	headerLines := 3
	footerLines := 1
	contentH := max(1, innerH-headerLines-footerLines)

	if m.importPreviewText == "" {
		b.WriteString(clipLine(empty.Render("  (no file selected)"), innerW) + "\n")
		for i := 1; i < contentH; i++ {
			b.WriteString("\n")
		}
		b.WriteString(clipLine(muted.Render(" Shift+J/K scroll"), innerW))
		return b.String()
	}

	lines := strings.Split(m.importPreviewText, "\n")
	maxScroll := max(0, len(lines)-contentH)
	start := clamp(m.importPreviewScroll, 0, maxScroll)
	end := min(len(lines), start+contentH)
	for _, line := range lines[start:end] {
		b.WriteString(clipLine(line, innerW) + "\n")
	}
	for i := end - start; i < contentH; i++ {
		b.WriteString("\n")
	}
	b.WriteString(clipLine(muted.Render(fmt.Sprintf(" Shift+J/K scroll  %d-%d/%d", start+1, end, len(lines))), innerW))
	return b.String()
}

var (
	brand              = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("57"))
	topbar             = lipgloss.NewStyle().Padding(0, 1).MarginBottom(1).Border(lipgloss.NormalBorder(), false, false, true, false).BorderForeground(lipgloss.Color("240"))
	chip               = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Background(lipgloss.Color("236"))
	selectedChip       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("16")).Background(lipgloss.Color("219"))
	activeFilterChip   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("16")).Background(lipgloss.Color("42"))
	inactiveFilterChip = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("240"))
	ascSortChip        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("16")).Background(lipgloss.Color("39"))
	descSortChip       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("16")).Background(lipgloss.Color("214"))
	okChip             = lipgloss.NewStyle().Foreground(lipgloss.Color("16")).Background(lipgloss.Color("42"))
	failChip           = lipgloss.NewStyle().Foreground(lipgloss.Color("16")).Background(lipgloss.Color("166"))
	errChip            = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(lipgloss.Color("160"))
	warnChip           = lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("94"))
	pane               = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("238")).Padding(0, 1).MarginRight(0)
	focusPane          = pane.BorderForeground(lipgloss.Color("63"))
	sel                = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(lipgloss.Color("62"))
	visStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(lipgloss.Color("57"))
	muted              = lipgloss.NewStyle().Foreground(lipgloss.Color("248"))
	subtle             = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	ok                 = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	inactive           = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	empty              = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Italic(true)
	box                = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("63")).Padding(1, 2)
	dangerBox          = box.BorderForeground(lipgloss.Color("203"))
	vacuumBox          = box.BorderForeground(lipgloss.Color("208"))
	restoreBox         = box.BorderForeground(lipgloss.Color("220"))
	footer             = lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Border(lipgloss.NormalBorder(), true, false, false, false).BorderForeground(lipgloss.Color("240")).MarginTop(1)
	sectionTitle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	tableHead          = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Border(lipgloss.NormalBorder(), false, false, true, false).BorderForeground(lipgloss.Color("238"))
	importHead         = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("238")).Padding(0, 1)
	activeGroup        = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(lipgloss.Color("237"))
)
