// Package cli owns command-line parsing and the lifecycle of the temporary proxy.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Goryudyuma/gomod-cooldown/internal/availability"
	"github.com/Goryudyuma/gomod-cooldown/internal/goindex"
	"github.com/Goryudyuma/gomod-cooldown/internal/proxy"
)

type Options struct {
	Cooldown        time.Duration
	Upstream        string
	TimeSource      string
	UpstreamTimeout time.Duration
	Verbose         bool
	Command         []string
}

func Parse(args []string, stderr io.Writer) (Options, error) {
	sep := -1
	for i, arg := range args {
		if arg == "--" {
			sep = i
			break
		}
	}
	if sep < 0 || sep == len(args)-1 {
		return Options{}, errors.New("a command after -- is required")
	}
	if args[sep+1] == "" {
		return Options{}, errors.New("command must not be empty")
	}
	fs := flag.NewFlagSet("gomod-cooldown", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cooldown := fs.String("cooldown", "14d", "minimum availability age")
	upstream := fs.String("upstream", "https://proxy.golang.org", "upstream GOPROXY URL")
	timeSource := fs.String("time-source", "commit", "availability source: commit (default) or combined")
	timeout := fs.Duration("upstream-timeout", 30*time.Second, "upstream HTTP timeout")
	verbose := fs.Bool("verbose", false, "log upstream requests and decisions")
	if err := fs.Parse(args[:sep]); err != nil {
		return Options{}, err
	}
	if fs.NArg() != 0 {
		return Options{}, fmt.Errorf("unexpected argument %q before --", fs.Arg(0))
	}
	d, err := ParseCooldown(*cooldown)
	if err != nil {
		return Options{}, err
	}
	if *timeout <= 0 {
		return Options{}, errors.New("upstream-timeout must be positive")
	}
	if *timeSource != "combined" && *timeSource != "commit" {
		return Options{}, fmt.Errorf("unsupported time-source %q", *timeSource)
	}
	return Options{Cooldown: d, Upstream: *upstream, TimeSource: *timeSource, UpstreamTimeout: *timeout, Verbose: *verbose, Command: args[sep+1:]}, nil
}

// ParseCooldown accepts time.ParseDuration plus a day suffix, where one day is
// exactly 24 hours. Months and years deliberately have no meaning here.
func ParseCooldown(s string) (time.Duration, error) {
	if s == "" {
		return 0, errors.New("cooldown is required")
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] >= '0' && s[i] <= '9' {
			j := i
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			if j < len(s) && s[j] == 'd' {
				days, err := strconv.ParseInt(s[i:j], 10, 64)
				if err != nil || days > int64(time.Duration(1<<63-1)/(24*time.Hour)) {
					return 0, fmt.Errorf("invalid cooldown %q", s)
				}
				b.WriteString(strconv.FormatInt(days*24, 10))
				b.WriteByte('h')
				i = j + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	d, err := time.ParseDuration(b.String())
	if err != nil {
		return 0, fmt.Errorf("invalid cooldown %q: %w", s, err)
	}
	if d <= 0 {
		return 0, errors.New("cooldown must be positive")
	}
	return d, nil
}

func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	opts, err := Parse(args, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "gomod-cooldown: %v\n", err)
		return 2
	}
	if err := run(ctx, opts, stdin, stdout, stderr); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		fmt.Fprintf(stderr, "gomod-cooldown: %v\n", err)
		return 1
	}
	return 0
}

func run(ctx context.Context, opts Options, stdin io.Reader, stdout, stderr io.Writer) error {
	client := &http.Client{Timeout: opts.UpstreamTimeout}
	started := time.Now()
	clock := func() time.Time { return started }
	var source availability.Source
	if opts.TimeSource == "commit" {
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
	p, err := proxy.New(proxy.Config{Upstream: opts.Upstream, Client: client, Source: source, Cooldown: opts.Cooldown, Now: clock, Logger: log.New(stderr, "gomod-cooldown: ", 0), Verbose: opts.Verbose})
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen on loopback: %w", err)
	}
	srv := &http.Server{Handler: p}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	cmd := exec.CommandContext(ctx, opts.Command[0], opts.Command[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.Env = withGOPROXY(os.Environ(), "http://"+ln.Addr().String())
	return cmd.Run()
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
