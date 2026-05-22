//go:build windows

package web

import (
	"os"
	"os/exec"
)

func startPTY(cmd *exec.Cmd) (*os.File, error) {
	// Windows web terminal needs ConPTY support. Pipe mode breaks Bubble Tea rendering,
	// so fail loudly instead of hanging after login.
	return nil, os.ErrInvalid
}

func resizePTY(f *os.File, cols, rows int) error { return nil }
