// Package cli owns command-line parsing and the lifecycle of the temporary proxy.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime/debug"
	"strings"
	"time"

	"github.com/Goryudyuma/gomod-cooldown/internal/availability"
	"github.com/Goryudyuma/gomod-cooldown/internal/goindex"
	"github.com/Goryudyuma/gomod-cooldown/internal/proxy"
)

const timeSourceCommit = "commit"

type action uint8

const (
	actionRun action = iota
	actionHelp
	actionVersion
)

// Options contains the parsed CLI configuration and child command.
type Options struct {
	Cooldown        time.Duration
	Upstream        string
	TimeSource      string
	UpstreamTimeout time.Duration
	Verbose         bool
	Command         []string
	action          action
}

// Parse parses command-line arguments without modifying the process environment.
func Parse(args []string, _ io.Writer) (Options, error) {
	sep := -1
	for i, arg := range args {
		if arg == "--" {
			sep = i
			break
		}
	}
	flagArgs := args
	if sep >= 0 {
		flagArgs = args[:sep]
	}
	fs, values := newFlagSet()
	err := fs.Parse(flagArgs)
	if err != nil {
		return Options{}, fmt.Errorf("parse flags: %w", err)
	}
	if fs.NArg() != 0 {
		return Options{}, fmt.Errorf("unexpected argument %q before --", fs.Arg(0))
	}
	if values.help {
		return Options{action: actionHelp}, nil
	}
	if values.version {
		return Options{action: actionVersion}, nil
	}
	if sep < 0 || sep == len(args)-1 {
		return Options{}, errors.New("a command after -- is required")
	}
	if args[sep+1] == "" {
		return Options{}, errors.New("command must not be empty")
	}
	d, err := ParseCooldown(values.cooldown)
	if err != nil {
		return Options{}, err
	}
	if values.timeout <= 0 {
		return Options{}, errors.New("upstream-timeout must be positive")
	}
	if values.timeSource != "combined" && values.timeSource != timeSourceCommit {
		return Options{}, fmt.Errorf("unsupported time-source %q", values.timeSource)
	}
	return Options{
		Cooldown: d, Upstream: values.upstream, TimeSource: values.timeSource,
		UpstreamTimeout: values.timeout, Verbose: values.verbose, Command: args[sep+1:],
	}, nil
}

type flagValues struct {
	cooldown   string
	upstream   string
	timeSource string
	timeout    time.Duration
	verbose    bool
	help       bool
	version    bool
}

func newFlagSet() (*flag.FlagSet, *flagValues) {
	values := &flagValues{}
	fs := flag.NewFlagSet("gomod-cooldown", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&values.cooldown, "cooldown", "14d", "minimum availability age")
	fs.StringVar(&values.upstream, "upstream", "https://proxy.golang.org", "upstream GOPROXY URL")
	fs.StringVar(&values.timeSource, "time-source", "commit", "availability source: commit (default) or combined")
	fs.DurationVar(&values.timeout, "upstream-timeout", 30*time.Second, "upstream HTTP timeout")
	fs.BoolVar(&values.verbose, "verbose", false, "log upstream requests and decisions")
	fs.BoolVar(&values.help, "help", false, "show this help and exit")
	fs.BoolVar(&values.help, "h", false, "show this help and exit")
	fs.BoolVar(&values.version, "version", false, "show version and exit")
	return fs, values
}

func writeUsage(w io.Writer) {
	_, _ = io.WriteString(w, `Usage: gomod-cooldown [options] -- command [args...]

Run a command with a temporary GOPROXY that hides module versions still in cooldown.

Options:
  --cooldown duration         Minimum availability age; accepts fractional days (default: 14d)
  --upstream URL              Upstream GOPROXY URL (default: https://proxy.golang.org)
  --time-source value         Availability source: commit or combined (default: commit)
  --upstream-timeout duration Upstream HTTP timeout (default: 30s)
  --verbose                   Log upstream requests and decisions
  -h, --help                  Show this help and exit
  --version                   Show version and exit
`)
}

// ParseCooldown accepts time.ParseDuration plus a day suffix, where one day is
// exactly 24 hours. Months and years deliberately have no meaning here.
func ParseCooldown(s string) (time.Duration, error) {
	if s == "" {
		return 0, errors.New("cooldown is required")
	}
	normalized, err := normalizeDayUnits(s)
	if err != nil {
		return 0, fmt.Errorf("invalid cooldown %q: %w", s, err)
	}
	d, err := time.ParseDuration(normalized)
	if err != nil {
		return 0, fmt.Errorf("invalid cooldown %q: %w", s, err)
	}
	if d <= 0 {
		return 0, errors.New("cooldown must be positive")
	}
	return d, nil
}

func normalizeDayUnits(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); {
		end, ok := scanDecimal(s, i)
		if !ok {
			b.WriteByte(s[i])
			i++
			continue
		}
		if end >= len(s) || s[end] != 'd' || !dayNumberCanStart(s, i) {
			b.WriteString(s[i:end])
			i = end
			continue
		}
		nanoseconds, err := dayNanoseconds(s[i:end])
		if err != nil {
			return "", err
		}
		b.WriteString(nanoseconds)
		b.WriteString("ns")
		i = end + 1
	}
	return b.String(), nil
}

func dayNumberCanStart(s string, start int) bool {
	if start == 0 || s[start] != '.' {
		return true
	}
	if start == 1 && (s[0] == '+' || s[0] == '-') {
		return true
	}
	previous := s[start-1]
	return previous == 'd' || previous == 'h' || previous == 'm' || previous == 's'
}

func scanDecimal(s string, start int) (int, bool) {
	i := start
	digits := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
		digits++
	}
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
			digits++
		}
	}
	return i, digits > 0
}

func dayNanoseconds(decimal string) (string, error) {
	days, ok := new(big.Rat).SetString(decimal)
	if !ok {
		return "", errors.New("invalid day value")
	}
	days.Mul(days, new(big.Rat).SetInt64(int64(24*time.Hour)))
	nanoseconds := new(big.Int).Quo(days.Num(), days.Denom())
	if !nanoseconds.IsInt64() {
		return "", errors.New("duration out of range")
	}
	return nanoseconds.String(), nil
}

// Run starts the proxy and child command, returning the child's exit code.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	opts, err := Parse(args, stderr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "gomod-cooldown: %v\n", err)
		_, _ = fmt.Fprintln(stderr, "Try 'gomod-cooldown --help' for usage.")
		return 2
	}
	switch opts.action {
	case actionHelp:
		writeUsage(stdout)
		return 0
	case actionVersion:
		_, _ = fmt.Fprintf(stdout, "gomod-cooldown %s\n", version())
		return 0
	case actionRun:
		// Continue below.
	}
	err = run(ctx, opts, stdin, stdout, stderr)
	if err != nil {
		var startErr *childStartError
		if errors.As(err, &startErr) {
			_, _ = fmt.Fprintf(stderr, "gomod-cooldown: %v\n", startErr)
			if startErr.notFound {
				return 127
			}
			return 126
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return childExitCode(exit)
		}
		_, _ = fmt.Fprintf(stderr, "gomod-cooldown: %v\n", err)
		return 1
	}
	return 0
}

func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return "devel"
	}
	return info.Main.Version
}

type childStartError struct {
	command  string
	err      error
	notFound bool
}

func (e *childStartError) Error() string {
	return fmt.Sprintf("start child command %q: %v", e.command, e.err)
}

func (e *childStartError) Unwrap() error { return e.err }

func run(ctx context.Context, opts Options, stdin io.Reader, stdout, stderr io.Writer) error {
	client := &http.Client{Timeout: opts.UpstreamTimeout}
	started := time.Now()
	clock := func() time.Time { return started }
	var source availability.Source
	if opts.TimeSource == timeSourceCommit {
		source = availability.CommitTimeSource{}
	} else {
		if strings.TrimRight(opts.Upstream, "/") != "https://proxy.golang.org" {
			return errors.New("time-source=combined requires --upstream=https://proxy.golang.org")
		}
		recent, _, err := (goindex.Fetcher{Client: client, Now: clock}).SnapshotForCooldown(ctx, opts.Cooldown)
		if err != nil {
			return fmt.Errorf("load complete index snapshot: %w", err)
		}
		source = availability.CombinedSource{Recent: recent}
	}
	p, err := proxy.New(proxy.Config{
		Upstream: opts.Upstream,
		Client:   client,
		Source:   source,
		Cooldown: opts.Cooldown,
		Now:      clock,
		Logger:   log.New(stderr, "gomod-cooldown: ", 0),
		Verbose:  opts.Verbose,
	})
	if err != nil {
		return fmt.Errorf("create proxy: %w", err)
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen on loopback: %w", err)
	}
	srv := &http.Server{Handler: p, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	return runChild(ctx, opts.Command, "http://"+ln.Addr().String(), stdin, stdout, stderr)
}

func runChild(ctx context.Context, command []string, proxyURL string, stdin io.Reader, stdout, stderr io.Writer) error {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, terminationSignals()...)
	defer signal.Stop(signals)
	//nolint:gosec // The caller explicitly supplies the argv after --; no shell is involved.
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.Env = withGOPROXY(os.Environ(), proxyURL)
	restoreForeground, processGroup := prepareChildProcess(cmd, stdin)
	defer restoreForeground()
	commandNotFound := errors.Is(cmd.Err, exec.ErrNotFound)
	if !commandNotFound {
		_, statErr := os.Stat(cmd.Path)
		commandNotFound = errors.Is(statErr, os.ErrNotExist)
	}
	cmd.Cancel = func() error {
		return cancelChildProcess(cmd.Process, processGroup)
	}
	if err := cmd.Start(); err != nil {
		return &childStartError{
			command:  command[0],
			err:      err,
			notFound: commandNotFound || errors.Is(err, exec.ErrNotFound),
		}
	}
	forwardingDone := make(chan struct{})
	forwardingStopped := make(chan struct{})
	go func() {
		defer close(forwardingStopped)
		forwardSignals(cmd.Process, processGroup, signals, forwardingDone)
	}()
	err := cmd.Wait()
	close(forwardingDone)
	<-forwardingStopped
	if err != nil {
		return fmt.Errorf("wait for child command: %w", err)
	}
	return nil
}

func forwardSignals(process *os.Process, processGroup bool, signals <-chan os.Signal, done <-chan struct{}) {
	for {
		select {
		case sig := <-signals:
			_ = forwardSignal(process, processGroup, sig)
		case <-done:
			return
		}
	}
}

func withGOPROXY(env []string, value string) []string {
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, "GOPROXY=") {
			result = append(result, entry)
		}
	}
	return append(result, "GOPROXY="+value)
}
