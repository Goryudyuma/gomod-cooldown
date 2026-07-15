package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const cacheHelperMarker = "gomod-cooldown-cache-helper"

const processHelperMarker = "gomod-cooldown-process-helper"

func TestParseCooldown(t *testing.T) {
	for _, tt := range []struct {
		name  string
		input string
		want  time.Duration
		bad   bool
	}{
		{name: "hours", input: "168h", want: 168 * time.Hour},
		{name: "integer days", input: "7d", want: 168 * time.Hour},
		{name: "decimal point without fraction", input: "1.d", want: 24 * time.Hour},
		{name: "fractional days", input: "1.5d", want: 36 * time.Hour},
		{name: "leading decimal", input: ".5d", want: 12 * time.Hour},
		{name: "signed leading decimal", input: "+.5d", want: 12 * time.Hour},
		{name: "leading decimal after unit", input: "1h.5d", want: 13 * time.Hour},
		{name: "compound", input: "14d12h", want: 348 * time.Hour},
		{name: "fractional compound", input: "1h1.25d30m", want: 31*time.Hour + 30*time.Minute},
		{name: "sub nanosecond truncation", input: "0.00000000000001d1ns", want: time.Nanosecond},
		{name: "fractional nanoseconds", input: "0.0000000000001d", want: 8 * time.Nanosecond},
		{name: "maximum duration", input: "106751d23h47m16.854775807s", want: time.Duration(1<<63 - 1)},
		{name: "empty", input: "", bad: true},
		{name: "months unsupported", input: "7M", bad: true},
		{name: "zero", input: "0", bad: true},
		{name: "zero fractional day", input: "0.00000000000001d", bad: true},
		{name: "negative", input: "-1h", bad: true},
		{name: "negative leading decimal", input: "-.5d", bad: true},
		{name: "embedded sign", input: "1d-1h", bad: true},
		{name: "exponent unsupported", input: "1e2d", bad: true},
		{name: "malformed decimal", input: "1..5d", bad: true},
		{name: "uppercase day unsupported", input: "1D", bad: true},
		{name: "day overflow", input: "106752d", bad: true},
		{name: "compound overflow", input: "106751d23h47m16.854775808s", bad: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCooldown(tt.input)
			if tt.bad {
				if err == nil {
					t.Fatalf("ParseCooldown(%q) succeeded with %v", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseCooldown(%q): %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("ParseCooldown(%q)=%v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseAndEnvironment(t *testing.T) {
	var errout bytes.Buffer
	o, err := Parse([]string{"--cooldown=7d", "--", "echo", "x"}, &errout)
	if err != nil || o.Cooldown != 7*24*time.Hour || o.TimeSource != "commit" || o.Command[0] != "echo" {
		t.Fatal(o, err)
	}
	help, err := Parse([]string{"--help"}, &errout)
	if err != nil || help.action != actionHelp {
		t.Fatalf("help=%+v err=%v", help, err)
	}
	version, err := Parse([]string{"--version"}, &errout)
	if err != nil || version.action != actionVersion {
		t.Fatalf("version=%+v err=%v", version, err)
	}
	for _, args := range [][]string{{}, {"--cooldown=0", "--", "x"}, {"--", ""}} {
		if _, err := Parse(args, &errout); err == nil {
			t.Fatalf("wanted error for %#v", args)
		}
	}
	env := withGOPROXY([]string{"A=B", "GOPROXY=old", "GOPRIVATE=x"}, "http://127.0.0.1:1")
	if strings.Join(env, " ") != "A=B GOPRIVATE=x GOPROXY=http://127.0.0.1:1" {
		t.Fatal(env)
	}
}

func TestRunHelpAndVersion(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"--help", "--", "must-not-run"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(context.Background(), args, nil, &stdout, &stderr); code != 0 {
			t.Fatalf("Run(%q)=%d, stderr=%q", args, code, stderr.String())
		}
		if !strings.HasPrefix(stdout.String(), "Usage: gomod-cooldown ") || !strings.Contains(stdout.String(), "-- command") {
			t.Fatalf("Run(%q) stdout=%q", args, stdout.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("Run(%q) stderr=%q", args, stderr.String())
		}
	}

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--version"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("version exit=%d stderr=%q", code, stderr.String())
	}
	if fields := strings.Fields(stdout.String()); len(fields) != 2 || fields[0] != "gomod-cooldown" || fields[1] == "" {
		t.Fatalf("version stdout=%q", stdout.String())
	}
	if strings.Count(stdout.String(), "\n") != 1 || stderr.Len() != 0 {
		t.Fatalf("version stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunUsageErrorIsPrintedOnce(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"--unknown"}, nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if stdout.Len() != 0 || strings.Count(stderr.String(), "flag provided but not defined") != 1 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.HasSuffix(stderr.String(), "Try 'gomod-cooldown --help' for usage.\n") {
		t.Fatalf("missing usage hint: %q", stderr.String())
	}
}

func TestExecutableHelpVersionAndChildExit(t *testing.T) {
	executable := buildCLIExecutable(t)
	for _, arg := range []string{"--help", "-h"} {
		var stdout, stderr bytes.Buffer
		cmd := exec.Command(executable, arg)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v, stderr=%q", arg, err, stderr.String())
		}
		if !strings.HasPrefix(stdout.String(), "Usage: gomod-cooldown ") || stderr.Len() != 0 {
			t.Fatalf("%s: stdout=%q stderr=%q", arg, stdout.String(), stderr.String())
		}
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(executable, "--version")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("version: %v, stderr=%q", err, stderr.String())
	}
	if fields := strings.Fields(stdout.String()); len(fields) != 2 || fields[0] != "gomod-cooldown" || stderr.Len() != 0 {
		t.Fatalf("version stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(
		executable, "--upstream=http://127.0.0.1:1", "--", testExecutable,
		"-test.run=^TestRunProcessHelper$", "-test.count=1", "--", processHelperMarker, "exit", "23",
	)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("child exit: err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}

func TestExecutablePreservesChildContractAndCleansUp(t *testing.T) {
	executable := buildCLIExecutable(t)
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(
		executable, "--upstream=http://127.0.0.1:1", "--", testExecutable,
		"-test.run=^TestRunProcessHelper$", "-test.count=1", "--", processHelperMarker, "contract", "one", "two words",
	)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "GOMOD_COOLDOWN_CONTRACT=from-executable")
	cmd.Stdin = strings.NewReader("black-box stdin")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("run executable: %v, stderr=%q", err, stderr.String())
	}
	var got struct {
		Args    []string
		CWD     string
		Env     string
		GOPROXY string
		Stdin   string
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode child output: %v: %q", err, stdout.String())
	}
	resolvedCWD, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	resolvedChildCWD, err := filepath.EvalSymlinks(got.CWD)
	if err != nil {
		t.Fatal(err)
	}
	cwdMatches := resolvedChildCWD == resolvedCWD
	if runtime.GOOS == "windows" {
		cwdMatches = strings.EqualFold(resolvedChildCWD, resolvedCWD)
	}
	if strings.Join(got.Args, "|") != "one|two words" || !cwdMatches || got.Env != "from-executable" || got.Stdin != "black-box stdin" {
		t.Fatalf("child contract=%+v, resolved cwd want %q", got, resolvedCWD)
	}
	if !strings.HasPrefix(got.GOPROXY, "http://127.0.0.1:") || stderr.String() != "child stderr\n" {
		t.Fatalf("GOPROXY=%q stderr=%q", got.GOPROXY, stderr.String())
	}
	client := &http.Client{Timeout: 500 * time.Millisecond}
	if response, requestErr := client.Get(got.GOPROXY); requestErr == nil {
		_ = response.Body.Close()
		t.Fatalf("proxy still accepts requests after executable returned: %s", got.GOPROXY)
	}
}

func buildCLIExecutable(t *testing.T) string {
	t.Helper()
	name := "gomod-cooldown"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", executable, "../../cmd/gomod-cooldown")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	return executable
}

func TestRunExitCodeAndDoesNotChangeEnvironment(t *testing.T) {
	if os.Getenv("GOPROXY") == "" {
		t.Setenv("GOPROXY", "https://proxy.example")
	}
	before := os.Getenv("GOPROXY")
	var out, err bytes.Buffer
	code := runProcessHelper(t, strings.NewReader(""), &out, &err, "exit-with-proxy", "7")
	if code != 7 {
		t.Fatalf("code=%d stderr=%s", code, err.String())
	}
	if os.Getenv("GOPROXY") != before {
		t.Fatal("parent environment changed")
	}
}

func TestRunConnectsStandardStreamsAndDoesNotStartAfterSetupFailure(t *testing.T) {
	var out, err bytes.Buffer
	code := runProcessHelper(t, strings.NewReader("hello\n"), &out, &err, "streams")
	if code != 0 || out.String() != "out:hello\n" || err.String() != "err:hello\n" {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), err.String())
	}
	out.Reset()
	err.Reset()
	executable, executableErr := os.Executable()
	if executableErr != nil {
		t.Fatal(executableErr)
	}
	code = Run(context.Background(), []string{"--time-source=combined", "--upstream=http://example.invalid", "--", executable, "-test.run=^TestRunProcessHelper$", "--", processHelperMarker, "exit", "7"}, nil, &out, &err)
	if code != 1 || strings.Contains(err.String(), "exit status 7") {
		t.Fatalf("code=%d err=%q", code, err.String())
	}
}

func TestRunChildContractAndProxyCleanup(t *testing.T) {
	t.Setenv("GOMOD_COOLDOWN_CONTRACT", "preserved")
	wantCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runProcessHelper(t, strings.NewReader("input bytes"), &stdout, &stderr, "contract", "alpha", "two words")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	var got struct {
		Args    []string
		CWD     string
		Env     string
		GOPROXY string
		Stdin   string
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode child output: %v: %q", err, stdout.String())
	}
	if strings.Join(got.Args, "|") != "alpha|two words" || got.CWD != wantCWD || got.Env != "preserved" || got.Stdin != "input bytes" {
		t.Fatalf("child contract=%+v, cwd want %q", got, wantCWD)
	}
	if !strings.HasPrefix(got.GOPROXY, "http://127.0.0.1:") {
		t.Fatalf("GOPROXY=%q", got.GOPROXY)
	}
	if stderr.String() != "child stderr\n" {
		t.Fatalf("stderr=%q", stderr.String())
	}
	client := &http.Client{Timeout: 500 * time.Millisecond}
	if response, requestErr := client.Get(got.GOPROXY); requestErr == nil {
		_ = response.Body.Close()
		t.Fatalf("proxy still accepts requests after Run returned: %s", got.GOPROXY)
	}
}

func TestRunCommandNotFound(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"--", "gomod-cooldown-command-that-does-not-exist"}, nil, &stdout, &stderr)
	if code != 127 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if stdout.Len() != 0 || strings.Count(stderr.String(), "gomod-cooldown:") != 1 || !strings.Contains(stderr.String(), "gomod-cooldown-command-that-does-not-exist") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunProcessHelper(t *testing.T) {
	marker := -1
	for i, arg := range os.Args {
		if arg == processHelperMarker {
			marker = i
			break
		}
	}
	if marker < 0 {
		return
	}
	args := os.Args[marker+1:]
	if len(args) == 0 {
		t.Fatal("helper mode is required")
	}
	switch args[0] {
	case "exit", "exit-with-proxy":
		runExitHelper(t, args)
	case "streams":
		runStreamsHelper(t)
	case "contract":
		runContractHelper(t, args[1:])
	default:
		t.Fatalf("unknown helper mode %q", args[0])
	}
}

func runExitHelper(t *testing.T, args []string) {
	t.Helper()
	if len(args) != 2 {
		t.Fatalf("exit helper args=%q", args)
	}
	code, err := strconv.Atoi(args[1])
	if err != nil {
		t.Fatal(err)
	}
	if args[0] == "exit-with-proxy" && !strings.HasPrefix(os.Getenv("GOPROXY"), "http://127.0.0.1:") {
		os.Exit(9)
	}
	os.Exit(code)
}

func runStreamsHelper(t *testing.T) {
	t.Helper()
	contents, err := io.ReadAll(os.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSuffix(string(contents), "\n")
	_, _ = fmt.Fprintf(os.Stdout, "out:%s\n", line)
	_, _ = fmt.Fprintf(os.Stderr, "err:%s\n", line)
	os.Exit(0)
}

func runContractHelper(t *testing.T, args []string) {
	t.Helper()
	contents, err := io.ReadAll(os.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	payload := struct {
		Args    []string
		CWD     string
		Env     string
		GOPROXY string
		Stdin   string
	}{
		Args: args, CWD: cwd, Env: os.Getenv("GOMOD_COOLDOWN_CONTRACT"),
		GOPROXY: os.Getenv("GOPROXY"), Stdin: string(contents),
	}
	if err := json.NewEncoder(os.Stdout).Encode(payload); err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintln(os.Stderr, "child stderr")
	os.Exit(0)
}

func runProcessHelper(t *testing.T, stdin io.Reader, stdout, stderr io.Writer, args ...string) int {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := []string{
		"--time-source=commit", "--upstream=http://127.0.0.1:1", "--", executable,
		"-test.run=^TestRunProcessHelper$", "-test.count=1", "--", processHelperMarker,
	}
	command = append(command, args...)
	return Run(context.Background(), command, stdin, stdout, stderr)
}

func TestRunInfoCacheLifetime(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("reuses metadata within one invocation", func(t *testing.T) {
		upstream, calls := newChangingInfoUpstream(t)
		defer upstream.Close()

		runCacheChild(t, executable, upstream.URL, "v1.0.0", "v1.0.0")
		if calls.list.Load() != 2 || calls.info.Load() != 1 {
			t.Fatalf("list calls=%d info calls=%d", calls.list.Load(), calls.info.Load())
		}
	})

	t.Run("starts empty on the next invocation", func(t *testing.T) {
		upstream, calls := newChangingInfoUpstream(t)
		defer upstream.Close()

		runCacheChild(t, executable, upstream.URL, "v1.0.0")
		runCacheChild(t, executable, upstream.URL, "")
		if calls.list.Load() != 2 || calls.info.Load() != 2 {
			t.Fatalf("list calls=%d info calls=%d", calls.list.Load(), calls.info.Load())
		}
	})
}

func TestRunInfoCacheHTTPHelper(t *testing.T) {
	marker := -1
	for i, arg := range os.Args {
		if arg == cacheHelperMarker {
			marker = i
			break
		}
	}
	if marker < 0 {
		return
	}

	proxyURL := strings.TrimRight(os.Getenv("GOPROXY"), "/")
	if proxyURL == "" {
		t.Fatal("GOPROXY is empty")
	}
	client := &http.Client{Timeout: 2 * time.Second}
	for requestIndex, want := range os.Args[marker+1:] {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, proxyURL+"/example.com/m/@v/list", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", requestIndex, err)
		}
		body, readErr := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("request %d: read=%v close=%v", requestIndex, readErr, closeErr)
		}
		if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != want {
			t.Fatalf("request %d: status=%d body=%q want=%q", requestIndex, resp.StatusCode, body, want)
		}
	}
}

type cacheUpstreamCalls struct {
	list atomic.Int32
	info atomic.Int32
}

func newChangingInfoUpstream(t *testing.T) (*httptest.Server, *cacheUpstreamCalls) {
	t.Helper()
	calls := &cacheUpstreamCalls{}
	started := time.Now().UTC().Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/example.com/m/@v/list":
			calls.list.Add(1)
			_, _ = io.WriteString(w, "v1.0.0\n")
		case "/example.com/m/@v/v1.0.0.info":
			call := calls.info.Add(1)
			stamp := started.Add(-30 * 24 * time.Hour)
			if call > 1 {
				stamp = started
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"Version":"v1.0.0","Time":%q}`, stamp.Format(time.RFC3339))
		default:
			http.NotFound(w, r)
		}
	}))
	return server, calls
}

func runCacheChild(t *testing.T, executable, upstream string, wants ...string) {
	t.Helper()
	args := []string{
		"--time-source=commit",
		"--cooldown=14d",
		"--upstream=" + upstream,
		"--upstream-timeout=2s",
		"--",
		executable,
		"-test.run=^TestRunInfoCacheHTTPHelper$",
		"-test.count=1",
		"--",
		cacheHelperMarker,
	}
	args = append(args, wants...)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var stdout bytes.Buffer
	var stderr lockedBuffer
	if code := Run(ctx, args, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("child exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.b.Write(p)
	if err != nil {
		return n, fmt.Errorf("write locked buffer: %w", err)
	}
	return n, nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}
