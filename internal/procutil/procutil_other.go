//go:build !unix

package procutil

import (
	"os"
	"os/exec"
	"time"
)

func setProcessGroup(cmd *exec.Cmd) {}

func killGroup(pid int, _ time.Duration) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	return p.Kill()
}
