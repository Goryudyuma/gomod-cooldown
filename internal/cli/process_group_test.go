//go:build darwin || linux

package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	processGroupReadyEnvironment            = "GOMOD_COOLDOWN_PROCESS_GROUP_READY"
	processGroupRoleEnvironment             = "GOMOD_COOLDOWN_PROCESS_GROUP_ROLE"
	processGroupDescendantReadyEnvironment  = "GOMOD_COOLDOWN_PROCESS_GROUP_DESCENDANT_READY"
	processGroupChildResultEnvironment      = "GOMOD_COOLDOWN_PROCESS_GROUP_CHILD_RESULT"
	processGroupDescendantResultEnvironment = "GOMOD_COOLDOWN_PROCESS_GROUP_DESCENDANT_RESULT"
	ttyHelperReadyEnvironment               = "GOMOD_COOLDOWN_TTY_HELPER_READY"
)

type processGroupReady struct {
	ProxyURL        string `json:"proxy_url"`
	ChildPID        int    `json:"child_pid"`
	ChildGroup      int    `json:"child_group"`
	DescendantPID   int    `json:"descendant_pid"`
	DescendantGroup int    `json:"descendant_group"`
}

type processIdentity struct {
	PID   int `json:"pid"`
	Group int `json:"group"`
}

type signalResult struct {
	PID    int `json:"pid"`
	Signal int `json:"signal"`
	Count  int `json:"count"`
}

func TestRunForwardsExactlyOneSignalToChildProcessGroup(t *testing.T) {
	wrapper := buildCLIExecutable(t)
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, delivery := range []string{"wrapper PID", "wrapper process group"} {
		for _, tt := range []struct {
			name string
			sig  syscall.Signal
			code int
		}{
			{name: "interrupt", sig: syscall.SIGINT, code: 130},
			{name: "terminate", sig: syscall.SIGTERM, code: 143},
		} {
			t.Run(delivery+"/"+tt.name, func(t *testing.T) {
				testProcessGroupSignal(t, wrapper, testExecutable, delivery, tt.sig, tt.code)
			})
		}
	}
}

func testProcessGroupSignal(
	t *testing.T,
	wrapper string,
	testExecutable string,
	delivery string,
	sig syscall.Signal,
	wantCode int,
) {
	t.Helper()
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready.json")
	descendantReady := filepath.Join(dir, "descendant-ready.json")
	childResult := filepath.Join(dir, "child-result.json")
	descendantResult := filepath.Join(dir, "descendant-result.json")
	var stderr lockedBuffer
	cmd := processGroupSignalCommand(
		wrapper, testExecutable, ready, descendantReady, childResult, descendantResult, &stderr,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	state := readJSONEventually[processGroupReady](t, ready, 10*time.Second, &stderr)
	cleanup := func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if state.ChildGroup > 0 {
			_ = syscall.Kill(-state.ChildGroup, syscall.SIGKILL)
		}
	}
	t.Cleanup(cleanup)

	validateProcessGroupState(t, state)

	var signalErr error
	if delivery == "wrapper process group" {
		signalErr = syscall.Kill(-cmd.Process.Pid, sig)
	} else {
		signalErr = cmd.Process.Signal(sig)
	}
	if signalErr != nil {
		t.Fatalf("deliver %s: %v", sig, signalErr)
	}
	waitErr := waitCommand(t, cmd, cleanup)
	var exit *exec.ExitError
	if !errors.As(waitErr, &exit) || exit.ExitCode() != wantCode {
		t.Fatalf("wait=%v exit=%v, want %d; stderr=%q", waitErr, exit, wantCode, stderr.String())
	}

	child := readJSONEventually[signalResult](t, childResult, 2*time.Second, &stderr)
	descendant := readJSONEventually[signalResult](t, descendantResult, 2*time.Second, &stderr)
	assertSignalResults(t, sig, child, descendant)
	waitForProcessGone(t, state.ChildPID, 2*time.Second)
	waitForProcessGone(t, state.DescendantPID, 2*time.Second)
	assertProxyClosed(t, state.ProxyURL)
}

func processGroupSignalCommand(
	wrapper string,
	testExecutable string,
	ready string,
	descendantReady string,
	childResult string,
	descendantResult string,
	stderr *lockedBuffer,
) *exec.Cmd {
	cmd := exec.Command(
		wrapper,
		"--time-source=commit",
		"--upstream=http://127.0.0.1:1",
		"--",
		testExecutable,
		"-test.run=^TestRunProcessGroupSignalHelper$",
		"-test.count=1",
	)
	cmd.Env = append(
		os.Environ(),
		processGroupReadyEnvironment+"="+ready,
		processGroupRoleEnvironment+"=child",
		processGroupDescendantReadyEnvironment+"="+descendantReady,
		processGroupChildResultEnvironment+"="+childResult,
		processGroupDescendantResultEnvironment+"="+descendantResult,
	)
	cmd.Stdout, cmd.Stderr = io.Discard, stderr
	// Isolate the wrapper so a simulated terminal process-group signal cannot
	// reach the test runner. The wrapper must then relay it to a different group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

func validateProcessGroupState(t *testing.T, state processGroupReady) {
	t.Helper()
	if state.ChildPID != state.ChildGroup {
		t.Fatalf("child pid=%d process group=%d", state.ChildPID, state.ChildGroup)
	}
	if state.DescendantGroup != state.ChildGroup || state.DescendantPID == state.ChildPID {
		t.Fatalf("child=%+v", state)
	}
	if state.ProxyURL == "" {
		t.Fatal("helper did not report GOPROXY")
	}
}

func assertSignalResults(t *testing.T, sig syscall.Signal, child, descendant signalResult) {
	t.Helper()
	for name, got := range map[string]signalResult{"child": child, "descendant": descendant} {
		if got.Count != 1 || got.Signal != int(sig) {
			t.Errorf("%s signal result=%+v, want signal=%d count=1", name, got, sig)
		}
	}
}

func TestRunProcessGroupSignalHelper(t *testing.T) {
	ready := os.Getenv(processGroupReadyEnvironment)
	if ready == "" {
		return
	}
	role := os.Getenv(processGroupRoleEnvironment)
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	if role == "descendant" {
		identity := processIdentity{PID: os.Getpid(), Group: syscall.Getpgrp()}
		writeHelperJSON(os.Getenv(processGroupDescendantReadyEnvironment), identity)
		sig, count := collectSignals(signals, 300*time.Millisecond)
		writeHelperJSON(os.Getenv(processGroupDescendantResultEnvironment), signalResult{
			PID: identity.PID, Signal: int(sig), Count: count,
		})
		os.Exit(0)
	}
	if role != "child" {
		_, _ = fmt.Fprintf(os.Stderr, "unknown process-group helper role %q\n", role)
		os.Exit(90)
	}

	descendant := exec.Command(
		os.Args[0],
		"-test.run=^TestRunProcessGroupSignalHelper$",
		"-test.count=1",
	)
	descendant.Env = replaceEnvironment(os.Environ(), processGroupRoleEnvironment, "descendant")
	descendant.Stdout, descendant.Stderr = os.Stdout, os.Stderr
	if err := descendant.Start(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "start signal descendant: %v\n", err)
		os.Exit(91)
	}
	descendantIdentity, err := readHelperJSON[processIdentity](
		os.Getenv(processGroupDescendantReadyEnvironment),
		5*time.Second,
	)
	if err != nil {
		_ = descendant.Process.Kill()
		_, _ = fmt.Fprintf(os.Stderr, "wait for signal descendant: %v\n", err)
		os.Exit(92)
	}
	writeHelperJSON(ready, processGroupReady{
		ProxyURL:        os.Getenv("GOPROXY"),
		ChildPID:        os.Getpid(),
		ChildGroup:      syscall.Getpgrp(),
		DescendantPID:   descendantIdentity.PID,
		DescendantGroup: descendantIdentity.Group,
	})

	sig, count := collectSignals(signals, 300*time.Millisecond)
	writeHelperJSON(os.Getenv(processGroupChildResultEnvironment), signalResult{
		PID: os.Getpid(), Signal: int(sig), Count: count,
	})
	if err := waitHelperCommand(descendant, 5*time.Second); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wait for signal descendant: %v\n", err)
		os.Exit(93)
	}
	os.Exit(128 + int(sig))
}

func collectSignals(signals <-chan os.Signal, settle time.Duration) (syscall.Signal, int) {
	value := <-signals
	first, ok := value.(syscall.Signal)
	if !ok {
		_, _ = fmt.Fprintf(os.Stderr, "unexpected signal type %T\n", value)
		os.Exit(98)
	}
	count := 1
	timer := time.NewTimer(settle)
	defer timer.Stop()
	for {
		select {
		case <-signals:
			count++
		case <-timer.C:
			return first, count
		}
	}
}

func TestRunKillsChildProcessGroupWhenContextIsCanceled(t *testing.T) {
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready.json")
	descendantReady := filepath.Join(dir, "descendant-ready.json")
	t.Setenv(processGroupReadyEnvironment, ready)
	t.Setenv(processGroupRoleEnvironment, "child")
	t.Setenv(processGroupDescendantReadyEnvironment, descendantReady)
	t.Setenv(processGroupChildResultEnvironment, filepath.Join(dir, "unused-child.json"))
	t.Setenv(processGroupDescendantResultEnvironment, filepath.Join(dir, "unused-descendant.json"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout bytes.Buffer
	var stderr lockedBuffer
	result := make(chan int, 1)
	go func() {
		result <- Run(ctx, []string{
			"--time-source=commit",
			"--upstream=http://127.0.0.1:1",
			"--",
			testExecutable,
			"-test.run=^TestRunProcessGroupSignalHelper$",
			"-test.count=1",
		}, nil, &stdout, &stderr)
	}()
	state := readJSONEventually[processGroupReady](t, ready, 10*time.Second, &stderr)
	cancel()
	select {
	case code := <-result:
		if code != 137 {
			t.Fatalf("Run after cancellation=%d, want 137; stderr=%q", code, stderr.String())
		}
	case <-time.After(10 * time.Second):
		_ = syscall.Kill(-state.ChildGroup, syscall.SIGKILL)
		t.Fatal("Run did not return after context cancellation")
	}
	waitForProcessGone(t, state.ChildPID, 5*time.Second)
	waitForProcessGone(t, state.DescendantPID, 5*time.Second)
	assertProxyClosed(t, state.ProxyURL)
}

type ttyHelperResult struct {
	PID             int    `json:"pid"`
	Group           int    `json:"group"`
	ForegroundGroup int    `json:"foreground_group"`
	Input           string `json:"input"`
}

func TestInteractiveChildOwnsTerminalAndReadsStdin(t *testing.T) {
	script, err := exec.LookPath("script")
	if err != nil {
		t.Skip("script utility is not installed")
	}
	wrapper := buildCLIExecutable(t)
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(t.TempDir(), "tty-ready.json")
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command(
			script,
			"-q",
			"/dev/null",
			wrapper,
			"--upstream=http://127.0.0.1:1",
			"--",
			testExecutable,
			"-test.run=^TestRunTTYHelper$",
			"-test.count=1",
		)
	} else {
		command := `exec "$GOMOD_COOLDOWN_TTY_WRAPPER" --upstream=http://127.0.0.1:1 -- "$GOMOD_COOLDOWN_TTY_TEST" -test.run=^TestRunTTYHelper$ -test.count=1`
		cmd = exec.Command(script, "-q", "-e", "-c", command, "/dev/null")
		cmd.Env = append(
			os.Environ(),
			"GOMOD_COOLDOWN_TTY_WRAPPER="+wrapper,
			"GOMOD_COOLDOWN_TTY_TEST="+testExecutable,
		)
	}
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, ttyHelperReadyEnvironment+"="+ready)
	inputReader, inputWriter := io.Pipe()
	cmd.Stdin = inputReader
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(inputWriter, "interactive input\n"); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("write pseudo-terminal input: %v", err)
	}
	got := readJSONEventually[ttyHelperResult](t, ready, 10*time.Second, nil)
	_ = inputWriter.Close()
	if err := waitCommand(t, cmd, func() { _ = cmd.Process.Kill() }); err != nil {
		t.Fatalf("run under pseudo-terminal: %v", err)
	}
	if got.PID != got.Group || got.ForegroundGroup != got.Group {
		t.Fatalf("TTY process identity=%+v", got)
	}
	if got.Input != "interactive input\n" {
		t.Fatalf("TTY input=%q", got.Input)
	}
}

func TestRunTTYHelper(t *testing.T) {
	ready := os.Getenv(ttyHelperReadyEnvironment)
	if ready == "" {
		return
	}
	foreground, err := terminalForegroundGroup(os.Stdin.Fd())
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "get terminal foreground group: %v\n", err)
		os.Exit(94)
	}
	input, err := bufio.NewReader(io.LimitReader(os.Stdin, 1<<20)).ReadString('\n')
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "read terminal input: %v\n", err)
		os.Exit(95)
	}
	writeHelperJSON(ready, ttyHelperResult{
		PID: os.Getpid(), Group: syscall.Getpgrp(), ForegroundGroup: foreground, Input: input,
	})
	os.Exit(0)
}

func waitCommand(t *testing.T, cmd *exec.Cmd, cleanup func()) error {
	t.Helper()
	const timeout = 10 * time.Second
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		cleanup()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		t.Fatalf("command did not exit within %s", timeout)
		return nil
	}
}

func waitHelperCommand(cmd *exec.Cmd, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return errors.New("helper command timed out")
	}
}

func readJSONEventually[T any](t *testing.T, path string, timeout time.Duration, stderr *lockedBuffer) T {
	t.Helper()
	value, err := readHelperJSON[T](path, timeout)
	if err != nil {
		var diagnostics string
		if stderr != nil {
			diagnostics = stderr.String()
		}
		t.Fatalf("read %s: %v; stderr=%q", path, err, diagnostics)
	}
	return value
}

func readHelperJSON[T any](path string, timeout time.Duration) (T, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(path)
		if err == nil {
			var value T
			if err := json.Unmarshal(contents, &value); err == nil {
				return value, nil
			} else {
				lastErr = err
			}
		} else if !os.IsNotExist(err) {
			lastErr = err
		}
		time.Sleep(10 * time.Millisecond)
	}
	var zero T
	return zero, fmt.Errorf("timed out after %s: %w", timeout, lastErr)
}

func writeHelperJSON(path string, value any) {
	contents, err := json.Marshal(value)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "marshal helper result: %v\n", err)
		os.Exit(96)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "write helper result: %v\n", err)
		os.Exit(97)
	}
}

func replaceEnvironment(env []string, key string, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func waitForProcessGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d still exists after %s", pid, timeout)
}

func assertProxyClosed(t *testing.T, proxyURL string) {
	t.Helper()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	if response, err := client.Get(proxyURL); err == nil {
		_ = response.Body.Close()
		t.Fatalf("proxy still accepts requests after wrapper exit: %s", proxyURL)
	}
}

func TestPrepareChildProcessUsesAGroupForNonTTYInput(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	restore, grouped := prepareChildProcess(cmd, strings.NewReader("not a terminal"))
	defer restore()
	if !grouped || cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid || cmd.SysProcAttr.Foreground {
		t.Fatalf("grouped=%v SysProcAttr=%+v", grouped, cmd.SysProcAttr)
	}
}

func TestTerminalForegroundGroupRejectsRegularFile(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "not-a-terminal"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if group, err := terminalForegroundGroup(file.Fd()); err == nil {
		t.Fatalf("regular file foreground group=%d", group)
	}
}
