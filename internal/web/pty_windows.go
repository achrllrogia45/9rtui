//go:build windows

package web

import (
	"os"
	"os/exec"
)

func startPTY(cmd *exec.Cmd) (*os.File, error) {
	// Fallback: pipe-like terminal. Full ConPTY support can replace this later.
	return nil, os.ErrInvalid
}
