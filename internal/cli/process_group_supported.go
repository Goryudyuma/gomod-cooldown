//go:build darwin || linux

package cli

import (
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"unsafe"
)

// prepareChildProcess isolates the child and its descendants in a process
// group. For an interactive invocation, the child group owns the terminal
// while it runs so reads from stdin and terminal-generated signals keep their
// normal shell semantics.
func prepareChildProcess(cmd *exec.Cmd, stdin io.Reader) (func(), bool) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	file, ok := stdin.(*os.File)
	if !ok {
		return func() {}, true
	}
	fd := file.Fd()
	wrapperGroup := syscall.Getpgrp()
	foregroundGroup, err := terminalForegroundGroup(fd)
	if err != nil || foregroundGroup != wrapperGroup {
		return func() {}, true
	}

	cmd.SysProcAttr.Foreground = true
	cmd.SysProcAttr.Ctty = int(fd)
	return func() {
		_ = setTerminalForegroundGroup(fd, wrapperGroup)
	}, true
}

// terminalForegroundGroup intentionally uses the standard library syscall
// package to avoid adding a runtime dependency solely for two terminal ioctls.
//
//nolint:gosec // The kernel writes an int32 to the pointer for TIOCGPGRP.
func terminalForegroundGroup(fd uintptr) (int, error) {
	var group int32
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		fd,
		uintptr(syscall.TIOCGPGRP),
		uintptr(unsafe.Pointer(&group)),
	)
	if errno != 0 {
		return 0, errno
	}
	return int(group), nil
}

//nolint:gosec // The kernel reads an int32 from the pointer for TIOCSPGRP.
func setTerminalForegroundGroup(fd uintptr, group int) error {
	group32 := int32(group)
	wasIgnored := signal.Ignored(syscall.SIGTTOU)
	signal.Ignore(syscall.SIGTTOU)
	if !wasIgnored {
		defer signal.Reset(syscall.SIGTTOU)
	}
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		fd,
		uintptr(syscall.TIOCSPGRP),
		uintptr(unsafe.Pointer(&group32)),
	)
	if errno != 0 {
		return errno
	}
	return nil
}
