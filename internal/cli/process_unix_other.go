//go:build aix || dragonfly || freebsd || illumos || netbsd || openbsd || solaris

package cli

import (
	"io"
	"os"
	"os/exec"
	"syscall"
)

func prepareChildProcess(cmd *exec.Cmd, stdin io.Reader) (func(), bool) {
	// Foreground terminal hand-off is implemented and tested on the supported
	// Darwin and Linux targets. Preserve interactive behavior on other Unix
	// targets instead of moving a TTY reader into a background process group.
	if file, ok := stdin.(*os.File); ok {
		info, err := file.Stat()
		if err == nil && info.Mode()&os.ModeCharDevice != 0 {
			return func() {}, false
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return func() {}, true
}
