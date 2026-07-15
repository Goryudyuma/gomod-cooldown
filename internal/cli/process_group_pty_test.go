//go:build darwin || linux

package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	ptyScriptTargetEnvironment    = "GOMOD_COOLDOWN_PTY_SCRIPT_TARGET"
	ptyScriptArgumentEnvironment  = "GOMOD_COOLDOWN_PTY_SCRIPT_ARG_"
	ptyInterruptReadyEnvironment  = "GOMOD_COOLDOWN_PTY_INTERRUPT_READY"
	ptyInterruptResultEnvironment = "GOMOD_COOLDOWN_PTY_INTERRUPT_RESULT"
	ptyRestoreReadyEnvironment    = "GOMOD_COOLDOWN_PTY_RESTORE_READY"
	ptyRestoreResultEnvironment   = "GOMOD_COOLDOWN_PTY_RESTORE_RESULT"
	ptyRestoreWrapperEnvironment  = "GOMOD_COOLDOWN_PTY_RESTORE_WRAPPER"
	ptyRestoreRoleEnvironment     = "GOMOD_COOLDOWN_PTY_RESTORE_ROLE"
	ptyRestoreMissingEnvironment  = "GOMOD_COOLDOWN_PTY_RESTORE_MISSING"
)

type ptyInterruptReady struct {
	ProxyURL        string `json:"proxy_url"`
	PID             int    `json:"pid"`
	Group           int    `json:"group"`
	ForegroundGroup int    `json:"foreground_group"`
}

func TestTerminalGeneratedInterruptIsDeliveredOnce(t *testing.T) {
	script := requireScriptUtility(t)
	wrapper := buildCLIExecutable(t)
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	readyPath := filepath.Join(dir, "interrupt-ready.json")
	resultPath := filepath.Join(dir, "interrupt-result.json")
	cmd := newPTYScriptCommand(
		script,
		wrapper,
		"--upstream=http://127.0.0.1:1",
		"--",
		testExecutable,
		"-test.run=^TestTerminalInterruptHelper$",
		"-test.count=1",
	)
	cmd.Env = replaceEnvironment(cmd.Env, ptyInterruptReadyEnvironment, readyPath)
	cmd.Env = replaceEnvironment(cmd.Env, ptyInterruptResultEnvironment, resultPath)
	inputReader, inputWriter := io.Pipe()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inputReader, io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var state ptyInterruptReady
	cleanup := func() {
		_ = inputWriter.Close()
		_ = cmd.Process.Kill()
		if state.Group > 0 {
			_ = syscall.Kill(-state.Group, syscall.SIGKILL)
		}
	}
	t.Cleanup(cleanup)

	state = readJSONEventually[ptyInterruptReady](t, readyPath, 10*time.Second, nil)
	if state.PID != state.Group || state.ForegroundGroup != state.Group {
		t.Fatalf("interactive child identity=%+v", state)
	}
	// ETX is the default VINTR character. Writing it to the pseudo-terminal's
	// master makes the terminal driver generate SIGINT for the foreground group.
	if _, err := inputWriter.Write([]byte{3}); err != nil {
		t.Fatalf("write terminal interrupt: %v", err)
	}
	result := readJSONEventually[signalResult](t, resultPath, 2*time.Second, nil)
	if result.Signal != int(syscall.SIGINT) || result.Count != 1 {
		t.Fatalf("terminal interrupt result=%+v", result)
	}
	_ = inputWriter.Close()
	waitErr := waitCommand(t, cmd, cleanup)
	assertExitCode(t, waitErr, 130)
	waitForProcessGone(t, state.PID, 2*time.Second)
	assertProxyClosed(t, state.ProxyURL)
}

func TestTerminalInterruptHelper(t *testing.T) {
	readyPath := os.Getenv(ptyInterruptReadyEnvironment)
	if readyPath == "" {
		return
	}
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGINT)
	foreground, err := terminalForegroundGroup(os.Stdin.Fd())
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "get interrupt helper foreground group: %v\n", err)
		os.Exit(100)
	}
	writeHelperJSON(readyPath, ptyInterruptReady{
		ProxyURL: os.Getenv("GOPROXY"), PID: os.Getpid(), Group: syscall.Getpgrp(), ForegroundGroup: foreground,
	})
	sig, count := collectSignals(signals, 300*time.Millisecond)
	writeHelperJSON(os.Getenv(ptyInterruptResultEnvironment), signalResult{
		PID: os.Getpid(), Signal: int(sig), Count: count,
	})
	os.Exit(130)
}

type ptyRestoreReady struct {
	PID             int `json:"pid"`
	Group           int `json:"group"`
	ForegroundGroup int `json:"foreground_group"`
	WrapperExit     int `json:"wrapper_exit"`
}

type ptyRestoreResult struct {
	Input string `json:"input"`
}

func TestForegroundGroupIsRestoredAfterChildFinishes(t *testing.T) {
	for _, tt := range []struct {
		name            string
		role            string
		wantWrapperExit int
	}{
		{name: "normal exit", role: "normal", wantWrapperExit: 0},
		{name: "start failure", role: "start-failure", wantWrapperExit: 127},
	} {
		t.Run(tt.name, func(t *testing.T) {
			testForegroundRestore(t, tt.role, tt.wantWrapperExit)
		})
	}
}

func testForegroundRestore(t *testing.T, role string, wantWrapperExit int) {
	t.Helper()
	script := requireScriptUtility(t)
	wrapper := buildCLIExecutable(t)
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	readyPath := filepath.Join(dir, "restore-ready.json")
	resultPath := filepath.Join(dir, "restore-result.json")
	missing := filepath.Join(dir, "command-that-does-not-exist")
	cmd := newPTYScriptCommand(
		script,
		testExecutable,
		"-test.run=^TestForegroundRestoreHelper$",
		"-test.count=1",
	)
	for key, value := range map[string]string{
		ptyRestoreReadyEnvironment:   readyPath,
		ptyRestoreResultEnvironment:  resultPath,
		ptyRestoreWrapperEnvironment: wrapper,
		ptyRestoreRoleEnvironment:    role,
		ptyRestoreMissingEnvironment: missing,
	} {
		cmd.Env = replaceEnvironment(cmd.Env, key, value)
	}
	inputReader, inputWriter := io.Pipe()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inputReader, io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		_ = inputWriter.Close()
		_ = cmd.Process.Kill()
	}
	t.Cleanup(cleanup)

	state := readJSONEventually[ptyRestoreReady](t, readyPath, 10*time.Second, nil)
	if state.WrapperExit != wantWrapperExit {
		t.Fatalf("wrapper exit=%d, want %d", state.WrapperExit, wantWrapperExit)
	}
	if state.PID != state.Group || state.ForegroundGroup != state.Group {
		t.Fatalf("terminal foreground was not restored: %+v", state)
	}
	if _, err := io.WriteString(inputWriter, "after wrapper\n"); err != nil {
		t.Fatalf("write post-wrapper input: %v", err)
	}
	result := readJSONEventually[ptyRestoreResult](t, resultPath, 2*time.Second, nil)
	if result.Input != "after wrapper\n" {
		t.Fatalf("post-wrapper input=%q", result.Input)
	}
	_ = inputWriter.Close()
	if err := waitCommand(t, cmd, cleanup); err != nil {
		t.Fatalf("PTY restore helper: %v", err)
	}
}

func TestForegroundRestoreHelper(t *testing.T) {
	readyPath := os.Getenv(ptyRestoreReadyEnvironment)
	if readyPath == "" {
		return
	}
	wrapperExit := runWrapperForRestoreHelper()
	foreground, err := terminalForegroundGroup(os.Stdin.Fd())
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "get restored foreground group: %v\n", err)
		os.Exit(101)
	}
	identity := ptyRestoreReady{
		PID: os.Getpid(), Group: syscall.Getpgrp(), ForegroundGroup: foreground, WrapperExit: wrapperExit,
	}
	writeHelperJSON(readyPath, identity)
	if identity.Group != identity.ForegroundGroup {
		os.Exit(102)
	}
	input, err := bufio.NewReader(io.LimitReader(os.Stdin, 1<<20)).ReadString('\n')
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "read after wrapper: %v\n", err)
		os.Exit(103)
	}
	writeHelperJSON(os.Getenv(ptyRestoreResultEnvironment), ptyRestoreResult{Input: input})
	os.Exit(0)
}

func runWrapperForRestoreHelper() int {
	wrapper := os.Getenv(ptyRestoreWrapperEnvironment)
	var child []string
	switch os.Getenv(ptyRestoreRoleEnvironment) {
	case "normal":
		child = []string{os.Args[0], "-test.run=^TestForegroundRestoreChild$", "-test.count=1"}
	case "start-failure":
		child = []string{os.Getenv(ptyRestoreMissingEnvironment)}
	default:
		return 104
	}
	args := []string{"--upstream=http://127.0.0.1:1", "--"}
	args = append(args, child...)
	cmd := exec.Command(wrapper, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, io.Discard, io.Discard
	err := cmd.Run()
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return 105
}

func TestForegroundRestoreChild(_ *testing.T) {}

func requireScriptUtility(t *testing.T) string {
	t.Helper()
	script, err := exec.LookPath("script")
	if err != nil {
		t.Skip("script utility is not installed")
	}
	return script
}

func newPTYScriptCommand(script, target string, args ...string) *exec.Cmd {
	if runtime.GOOS == "darwin" {
		scriptArgs := []string{"-q", "-e", "/dev/null", target}
		scriptArgs = append(scriptArgs, args...)
		cmd := exec.Command(script, scriptArgs...)
		cmd.Env = os.Environ()
		return cmd
	}

	env := replaceEnvironment(os.Environ(), ptyScriptTargetEnvironment, target)
	var command strings.Builder
	command.WriteString(`exec "$` + ptyScriptTargetEnvironment + `"`)
	for index, arg := range args {
		key := fmt.Sprintf("%s%d", ptyScriptArgumentEnvironment, index)
		env = replaceEnvironment(env, key, arg)
		command.WriteString(` "$` + key + `"`)
	}
	cmd := exec.Command(script, "-q", "-e", "-c", command.String(), "/dev/null")
	cmd.Env = env
	return cmd
}

func assertExitCode(t *testing.T, err error, want int) {
	t.Helper()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != want {
		t.Fatalf("exit error=%v, want status %d", err, want)
	}
}
