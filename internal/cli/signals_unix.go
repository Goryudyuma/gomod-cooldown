//go:build aix || darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || solaris

package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

func forwardSignal(process *os.Process, processGroup bool, sig os.Signal) error {
	if !processGroup {
		if err := process.Signal(sig); err != nil {
			return fmt.Errorf("forward %s to child: %w", sig, err)
		}
		return nil
	}
	syscallSignal, ok := sig.(syscall.Signal)
	if !ok {
		return fmt.Errorf("forward unsupported signal %T to child process group", sig)
	}
	if err := syscall.Kill(-process.Pid, syscallSignal); err != nil {
		return fmt.Errorf("forward %s to child: %w", sig, err)
	}
	return nil
}

func cancelChildProcess(process *os.Process, processGroup bool) error {
	err := forwardSignal(process, processGroup, os.Kill)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func childExitCode(exit *exec.ExitError) int {
	if code := exit.ExitCode(); code >= 0 {
		return code
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return 1
}
