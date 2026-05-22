//go:build !windows

package web

import (
	"os"
	"os/exec"

	"github.com/creack/pty"
)

func startPTY(cmd *exec.Cmd) (*os.File, error) {
	return pty.Start(cmd)
}
