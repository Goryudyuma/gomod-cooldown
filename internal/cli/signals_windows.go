//go:build windows

package cli

import (
	"fmt"
	"os"
	"os/exec"
)

func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

func forwardSignal(process *os.Process, _ bool, sig os.Signal) error {
	if err := process.Signal(sig); err == nil {
		return nil
	}
	// Windows cannot deliver os.Interrupt to arbitrary child processes. Ensure
	// the child does not outlive the wrapper when the console interrupts it.
	if err := process.Kill(); err != nil {
		return fmt.Errorf("stop child after %s: %w", sig, err)
	}
	return nil
}

func cancelChildProcess(process *os.Process, _ bool) error {
	return process.Kill()
}

func childExitCode(exit *exec.ExitError) int {
	if code := exit.ExitCode(); code >= 0 {
		return code
	}
	return 1
}
