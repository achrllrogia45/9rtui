package tui

func helpView() string {
	return box.Render(brand.Render(" Help / Hotkeys ") + "\n\n" +
		"Navigation\n" +
		"  j/k, arrows      move\n" +
		"  h/l              focus provider/accounts pane\n" +
		"  gg/G             top/bottom\n" +
		"\nAccounts\n" +
		"  Enter/i          inspect account JSON\n" +
		"  e                export inspected JSON to .accounts\n" +
		"  Space            toggle account selection\n" +
		"  v                visual select range\n" +
		"  a/A              select all visible / clear\n" +
		"  d                delete selected (writes undo log first)\n" +
		"  O                on/off selected\n" +
		"\nMenus\n" +
		"  b/B              recovery + restore logs\n" +
		"  /                more options (Indexing, Vacuum)\n" +
		"  U                update center\n" +
		"  I                import\n" +
		"\nSafety\n" +
		"  Index/Vacuum     yellow warning, force-stop 9Router, daily backup first\n" +
		"  Backup path      .9router/db/backup/data.sqlite.bak-YYYYMMDD-HHMM\n" +
		"\nGeneral\n" +
		"  r                refresh\n" +
		"  ?                this help\n" +
		"  Esc/q            back / quit")
}
