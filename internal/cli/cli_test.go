package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const cacheHelperMarker = "gomod-cooldown-cache-helper"

func TestParseCooldown(t *testing.T) {
	for _, tt := range []struct {
		s    string
		want time.Duration
		bad  bool
	}{
		{"168h", 168 * time.Hour, false}, {"7d", 168 * time.Hour, false}, {"14d12h", 348 * time.Hour, false}, {"", 0, true}, {"7M", 0, true}, {"0", 0, true}, {"-1h", 0, true},
	} {
		got, err := ParseCooldown(tt.s)
		if tt.bad {
			if err == nil {
				t.Fatal(tt.s)
			}
		} else if err != nil || got != tt.want {
			t.Fatalf("%s: %v %v", tt.s, got, err)
		}
	}
}

func TestParseAndEnvironment(t *testing.T) {
	var errout bytes.Buffer
	o, err := Parse([]string{"--cooldown=7d", "--", "echo", "x"}, &errout)
	if err != nil || o.Cooldown != 7*24*time.Hour || o.TimeSource != "commit" || o.Command[0] != "echo" {
		t.Fatal(o, err)
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

func TestRunExitCodeAndDoesNotChangeEnvironment(t *testing.T) {
	if os.Getenv("GOPROXY") == "" {
		t.Setenv("GOPROXY", "https://proxy.example")
	}
	before := os.Getenv("GOPROXY")
	var out, err bytes.Buffer
	// commit mode avoids external setup; the child is run with argv, not a shell.
	code := Run(context.Background(), []string{"--time-source=commit", "--upstream=http://127.0.0.1:1", "--", "sh", "-c", `case "$GOPROXY" in http://127.0.0.1:*) exit 7;; *) exit 9;; esac`}, nil, &out, &err)
	if code != 7 {
		t.Fatalf("code=%d stderr=%s", code, err.String())
	}
	if os.Getenv("GOPROXY") != before {
		t.Fatal("parent environment changed")
	}
}

func TestRunConnectsStandardStreamsAndDoesNotStartAfterSetupFailure(t *testing.T) {
	var out, err bytes.Buffer
	code := Run(context.Background(), []string{"--time-source=commit", "--upstream=http://127.0.0.1:1", "--", "sh", "-c", `read x; echo "out:$x"; echo "err:$x" >&2`}, strings.NewReader("hello\n"), &out, &err)
	if code != 0 || out.String() != "out:hello\n" || err.String() != "err:hello\n" {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), err.String())
	}
	out.Reset()
	err.Reset()
	code = Run(context.Background(), []string{"--time-source=combined", "--upstream=http://example.invalid", "--", "sh", "-c", "exit 7"}, nil, &out, &err)
	if code != 1 || strings.Contains(err.String(), "exit status 7") {
		t.Fatalf("code=%d err=%q", code, err.String())
	}
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
