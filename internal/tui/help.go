package tui

func helpView() string {
	return box.Render(brand.Render(" Hotkeys ") + "\n\n" + "j/k        move\nh/l        focus pane\ngg/G       top/bottom\nEnter/i    inspect account JSON\n  e        export inspected JSON to ./.accounts/\nSpace      toggle account\nv          visual select range\na/A        select all visible / clear\nd          delete selected (writes tiny ./.tui-logs undo log)\nb          recovery/maintenance\n  R        restore selected undo log\n  C        clean request/usage logs + VACUUM\n  F        full SQLite snapshot (.bak/.bak2)\nr          refresh\nq          quit\nEsc        cancel")
}
