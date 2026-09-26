//go:build unix

package procutil

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killGroup SIGTERMs process group pgid, then SIGKILLs it after grace. The
// short grace keeps the pgid-reuse window (leader reaped, pid recycled as a
// new group leader before the SIGKILL fires) negligible.
func killGroup(pgid int, grace time.Duration) error {
	if err := syscall.Kill(-pgid, syscall.SIGTERM); errors.Is(err, syscall.ESRCH) {
		return nil
	}
	time.AfterFunc(grace, func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	})
	return nil
}
