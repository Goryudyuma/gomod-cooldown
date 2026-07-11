package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

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
