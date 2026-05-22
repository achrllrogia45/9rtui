//go:build !windows

package web

import (
	"os"
	"os/exec"

	"github.com/creack/pty"
)

func startPTY(cmd *exec.Cmd) (*os.File, error) {
	f, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	_ = resizePTY(f, 120, 40)
	return f, nil
}

func resizePTY(f *os.File, cols, rows int) error {
	if cols < 20 || rows < 5 {
		return nil
	}
	return pty.Setsize(f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}
