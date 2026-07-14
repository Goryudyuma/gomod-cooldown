package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Goryudyuma/gomod-cooldown/internal/availability"
)

var now = time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)

func TestListFiltersAvailabilityAndOrder(t *testing.T) {
	commitOld := now.Add(-15 * 24 * time.Hour)
	commitNew := now.Add(-time.Hour)
	var infoCalls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/example.com/m/@v/list":
			io.WriteString(w, "v1.0.0\nv1.1.0\nv1.2.0\nv1.3.0\n")
		default:
			infoCalls.Add(1)
			v := r.URL.Path[len("/example.com/m/@v/"):]
			v = v[:len(v)-5]
			tm := commitOld
			if v == "v1.2.0" {
				tm = commitNew
			}
			fmt.Fprintf(w, `{"Version":%q,"Time":%q}`, v, tm.Format(time.RFC3339))
		}
	}))
	defer up.Close()
	firstNew := now.Add(-2 * time.Hour)
	s := newTestServer(t, up.URL, availability.CombinedSource{Recent: map[string]time.Time{
		availability.Key("example.com/m", "v1.1.0"): firstNew,
	}})
	r := httptest.NewRecorder()
	s.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/example.com/m/@v/list", nil))
	if r.Code != 200 || r.Body.String() != "v1.0.0\nv1.3.0\n" {
		t.Fatalf("status=%d body=%q", r.Code, r.Body.String())
	}
	if infoCalls.Load() != 4 {
		t.Fatalf("info calls = %d", infoCalls.Load())
	}
}

func TestBoundaryAndMalformedInfoAreNotSkipped(t *testing.T) {
	t.Run("boundary", func(t *testing.T) {
		up := fakeProxy(t, map[string]string{"/example.com/m/@v/list": "v1.0.0\n", "/example.com/m/@v/v1.0.0.info": info("v1.0.0", now.Add(-14*24*time.Hour))})
		defer up.Close()
		s := newTestServer(t, up.URL, availability.CommitTimeSource{})
		r := httptest.NewRecorder()
		s.ServeHTTP(r, httptest.NewRequest("GET", "/example.com/m/@v/list", nil))
		if r.Code != 200 || r.Body.String() != "v1.0.0\n" {
			t.Fatal(r.Code, r.Body.String())
		}
	})
	for name, raw := range map[string]string{"missing-time": `{"Version":"v1.0.0"}`, "null-time": `{"Version":"v1.0.0","Time":null}`, "zero-time": `{"Version":"v1.0.0","Time":"0001-01-01T00:00:00Z"}`, "bad-time": `{"Version":"v1.0.0","Time":"wat"}`, "missing-version": `{"Time":"2026-01-01T00:00:00Z"}`, "mismatch": `{"Version":"v1.0.1","Time":"2026-01-01T00:00:00Z"}`, "bad-json": `{`} {
		t.Run(name, func(t *testing.T) {
			up := fakeProxy(t, map[string]string{"/example.com/m/@v/list": "v1.0.0\n", "/example.com/m/@v/v1.0.0.info": raw})
			defer up.Close()
			s := newTestServer(t, up.URL, availability.CommitTimeSource{})
			r := httptest.NewRecorder()
			s.ServeHTTP(r, httptest.NewRequest("GET", "/example.com/m/@v/list", nil))
			if r.Code != 502 {
				t.Fatalf("got %d: %s", r.Code, r.Body.String())
			}
		})
	}
}

func TestDiscoveryDecodesUppercaseModulePathFromRealGoClient(t *testing.T) {
	up := fakeProxy(t, map[string]string{
		"/github.com/!azure/example/@v/list":        "v1.0.0\n",
		"/github.com/!azure/example/@v/v1.0.0.info": info("v1.0.0", now.Add(-time.Hour)),
	})
	defer up.Close()
	s := newTestServer(t, up.URL, availability.CommitTimeSource{})

	r := httptest.NewRecorder()
	s.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/github.com/%21azure/example/@v/list", nil))
	if r.Code != http.StatusOK || r.Body.String() != "" {
		t.Fatalf("status=%d body=%q", r.Code, r.Body.String())
	}
}

func TestPassthroughNeverChecksAvailability(t *testing.T) {
	var source callsSource
	up := fakeProxy(t, map[string]string{"/example.com/m/@v/v1.9.0.info": info("v1.9.0", now), "/example.com/m/@v/v1.9.0.mod": "module example.com/m\n", "/example.com/m/@v/v1.9.0.zip": "zip"})
	defer up.Close()
	s := newTestServer(t, up.URL, &source)
	for _, suffix := range []string{".info", ".mod", ".zip"} {
		r := httptest.NewRecorder()
		s.ServeHTTP(r, httptest.NewRequest("GET", "/example.com/m/@v/v1.9.0"+suffix, nil))
		if r.Code != 200 {
			t.Fatal(suffix, r.Code)
		}
	}
	if source.n.Load() != 0 {
		t.Fatal("availability source was called")
	}
}

func TestPassthroughPreservesHeaders(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Content-Type", "application/zip")
		io.WriteString(w, "zip")
	}))
	defer up.Close()
	s := newTestServer(t, up.URL, availability.CommitTimeSource{})
	r := httptest.NewRecorder()
	s.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/example.com/M/@v/v1.0.0.zip", nil))
	if r.Code != 200 || r.Header().Get("ETag") != `"abc"` || r.Body.String() != "zip" {
		t.Fatalf("status=%d headers=%v body=%q", r.Code, r.Header(), r.Body.String())
	}
}

func TestDoesNotFollowUpstreamRedirects(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer upstream.Close()
	s := newTestServer(t, upstream.URL, availability.CommitTimeSource{})
	r := httptest.NewRecorder()
	s.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/example.com/m/@v/list", nil))
	if r.Code != http.StatusBadGateway || redirected.Load() != 0 {
		t.Fatalf("status=%d followed=%d", r.Code, redirected.Load())
	}
	r = httptest.NewRecorder()
	s.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/example.com/m/@v/v1.0.0.mod", nil))
	if r.Code != http.StatusFound || r.Header().Get("Location") != target.URL || redirected.Load() != 0 {
		t.Fatalf("status=%d location=%q followed=%d", r.Code, r.Header().Get("Location"), redirected.Load())
	}
}

func TestLatestFallback(t *testing.T) {
	tests := []struct{ name, list, want string }{
		{"old-latest", "", "v9.0.0"},
		{"release", "v1.0.0\nv2.0.0-beta.1\n", "v1.0.0"},
		{"prerelease", "v1.0.0-beta.1\nv1.0.0-rc.1\n", "v1.0.0-rc.1"},
		{"pseudo", "v0.0.0-20260711100000-abcdefabcdef\n", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			latestTime := now
			if tt.name == "old-latest" {
				latestTime = now.Add(-20 * 24 * time.Hour)
			}
			m := map[string]string{"/example.com/m/@latest": info("v9.0.0", latestTime), "/example.com/m/@v/list": tt.list}
			for _, v := range []string{"v1.0.0", "v2.0.0-beta.1", "v1.0.0-beta.1", "v1.0.0-rc.1"} {
				m["/example.com/m/@v/"+v+".info"] = info(v, now.Add(-20*24*time.Hour))
			}
			up := fakeProxy(t, m)
			defer up.Close()
			s := newTestServer(t, up.URL, availability.CommitTimeSource{})
			r := httptest.NewRecorder()
			s.ServeHTTP(r, httptest.NewRequest("GET", "/example.com/m/@latest", nil))
			if tt.want == "" {
				if r.Code != 404 {
					t.Fatal(r.Code)
				}
			} else if r.Code != 200 || !bytes.Contains(r.Body.Bytes(), []byte(tt.want)) {
				t.Fatalf("%d %s", r.Code, r.Body.String())
			}
		})
	}
}

func TestLatestMalformedMetadataReturnsBadGateway(t *testing.T) {
	up := fakeProxy(t, map[string]string{"/example.com/m/@latest": `{"Version":"v1.0.0"}`})
	defer up.Close()
	s := newTestServer(t, up.URL, availability.CommitTimeSource{})
	r := httptest.NewRecorder()
	s.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/example.com/m/@latest", nil))
	if r.Code != http.StatusBadGateway {
		t.Fatalf("status=%d", r.Code)
	}
}

func TestInfoCacheReusesMetadataButReevaluatesDecision(t *testing.T) {
	current := now
	commit := now.Add(-13 * 24 * time.Hour)
	var listCalls, infoCalls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/example.com/m/@v/list":
			listCalls.Add(1)
			_, _ = io.WriteString(w, "v1.0.0\n")
		case "/example.com/m/@v/v1.0.0.info":
			infoCalls.Add(1)
			_, _ = io.WriteString(w, info("v1.0.0", commit))
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()

	newServer := func() *Server {
		s, err := New(Config{
			Upstream: up.URL,
			Source:   availability.CommitTimeSource{},
			Cooldown: 14 * 24 * time.Hour,
			Now:      func() time.Time { return current },
			Logger:   log.New(io.Discard, "", 0),
		})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	s := newServer()
	first := httptest.NewRecorder()
	s.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/example.com/m/@v/list", nil))
	if first.Code != http.StatusOK || first.Body.String() != "" {
		t.Fatalf("first status=%d body=%q", first.Code, first.Body.String())
	}

	current = current.Add(2 * 24 * time.Hour)
	second := httptest.NewRecorder()
	s.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/example.com/m/@v/list", nil))
	if second.Code != http.StatusOK || second.Body.String() != "v1.0.0\n" {
		t.Fatalf("second status=%d body=%q", second.Code, second.Body.String())
	}
	if listCalls.Load() != 2 || infoCalls.Load() != 1 {
		t.Fatalf("same server list calls=%d info calls=%d", listCalls.Load(), infoCalls.Load())
	}

	third := httptest.NewRecorder()
	newServer().ServeHTTP(third, httptest.NewRequest(http.MethodGet, "/example.com/m/@v/list", nil))
	if third.Code != http.StatusOK || third.Body.String() != "v1.0.0\n" {
		t.Fatalf("new server status=%d body=%q", third.Code, third.Body.String())
	}
	if listCalls.Load() != 3 || infoCalls.Load() != 2 {
		t.Fatalf("new server list calls=%d info calls=%d", listCalls.Load(), infoCalls.Load())
	}
}

func TestInfoCacheRetriesAfterUpstreamFailure(t *testing.T) {
	var infoCalls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/example.com/m/@v/list" {
			_, _ = io.WriteString(w, "v1.0.0\n")
			return
		}
		if infoCalls.Add(1) == 1 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, info("v1.0.0", now.Add(-20*24*time.Hour)))
	}))
	defer up.Close()
	s := newTestServer(t, up.URL, availability.CommitTimeSource{})

	first := httptest.NewRecorder()
	s.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/example.com/m/@v/list", nil))
	second := httptest.NewRecorder()
	s.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/example.com/m/@v/list", nil))
	if first.Code != http.StatusBadGateway || second.Code != http.StatusOK || second.Body.String() != "v1.0.0\n" {
		t.Fatalf("first=%d second=%d body=%q", first.Code, second.Code, second.Body.String())
	}
	if infoCalls.Load() != 2 {
		t.Fatalf("info calls=%d", infoCalls.Load())
	}
}

func TestConcurrentInfoCache(t *testing.T) {
	const requests = 30
	var listCalls, infoCalls atomic.Int32
	allListsStarted := make(chan struct{})
	infoStarted := make(chan struct{})
	releaseInfo := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseInfo) })
	defer release()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/example.com/m/@v/list" {
			if listCalls.Add(1) == requests {
				close(allListsStarted)
			}
			select {
			case <-allListsStarted:
			case <-time.After(2 * time.Second):
				http.Error(w, "list barrier timeout", http.StatusGatewayTimeout)
				return
			}
			_, _ = io.WriteString(w, "v1.0.0\n")
			return
		}
		if infoCalls.Add(1) == 1 {
			close(infoStarted)
		}
		select {
		case <-releaseInfo:
		case <-time.After(2 * time.Second):
			http.Error(w, "info barrier timeout", http.StatusGatewayTimeout)
			return
		}
		_, _ = io.WriteString(w, info("v1.0.0", now.Add(-20*24*time.Hour)))
	}))
	defer up.Close()
	s := newTestServer(t, up.URL, availability.CommitTimeSource{})
	start := make(chan struct{})
	done := make(chan int, requests)
	var ready sync.WaitGroup
	ready.Add(requests)
	for range requests {
		go func() {
			ready.Done()
			<-start
			r := httptest.NewRecorder()
			s.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/example.com/m/@v/list", nil))
			done <- r.Code
		}()
	}
	ready.Wait()
	close(start)
	receiveWithin(t, infoStarted, "upstream .info request")
	// Give every concurrent request time to join the same in-flight lookup.
	time.Sleep(50 * time.Millisecond)
	release()
	requireOKStatuses(t, done, requests)
	if infoCalls.Load() != 1 {
		t.Fatalf("info calls=%d", infoCalls.Load())
	}
}

func TestInfoCacheWaiterRetriesAfterLeaderCancellation(t *testing.T) {
	var infoCalls atomic.Int32
	firstStarted := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if infoCalls.Add(1) == 1 {
			close(firstStarted)
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, info("v1.0.0", now.Add(-20*24*time.Hour)))
	}))
	defer up.Close()
	s := newTestServer(t, up.URL, availability.CommitTimeSource{})

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()
	leaderDone := make(chan error, 1)
	go func() {
		_, err := s.info(leaderCtx, "example.com/m", "v1.0.0")
		leaderDone <- err
	}()
	receiveWithin(t, firstStarted, "leader request")

	waiterCtx := &doneObservingContext{Context: context.Background(), observed: make(chan struct{})}
	waiterDone := make(chan infoResult, 1)
	go func() {
		got, err := s.info(waiterCtx, "example.com/m", "v1.0.0")
		waiterDone <- infoResult{info: got, err: err}
	}()
	receiveWithin(t, waiterCtx.observed, "waiter join")
	cancelLeader()

	if err := receiveWithin(t, leaderDone, "leader cancellation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error=%v", err)
	}
	result := receiveWithin(t, waiterDone, "waiter retry")
	if result.err != nil || result.info.Version != "v1.0.0" {
		t.Fatalf("waiter info=%+v error=%v", result.info, result.err)
	}
	if infoCalls.Load() != 2 {
		t.Fatalf("info calls=%d", infoCalls.Load())
	}
}

func TestInfoCacheCanceledWaiterDoesNotCancelLeader(t *testing.T) {
	var infoCalls atomic.Int32
	firstStarted := make(chan struct{})
	releaseInfo := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseInfo) })
	defer release()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if infoCalls.Add(1) == 1 {
			close(firstStarted)
		}
		<-releaseInfo
		_, _ = io.WriteString(w, info("v1.0.0", now.Add(-20*24*time.Hour)))
	}))
	defer up.Close()
	s := newTestServer(t, up.URL, availability.CommitTimeSource{})

	leaderDone := make(chan error, 1)
	go func() {
		_, err := s.info(context.Background(), "example.com/m", "v1.0.0")
		leaderDone <- err
	}()
	receiveWithin(t, firstStarted, "leader request")

	waiterBase, cancelWaiter := context.WithCancel(context.Background())
	defer cancelWaiter()
	waiterCtx := &doneObservingContext{Context: waiterBase, observed: make(chan struct{})}
	waiterDone := make(chan error, 1)
	go func() {
		_, err := s.info(waiterCtx, "example.com/m", "v1.0.0")
		waiterDone <- err
	}()
	receiveWithin(t, waiterCtx.observed, "waiter join")
	cancelWaiter()
	if err := receiveWithin(t, waiterDone, "waiter cancellation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter error=%v", err)
	}

	release()
	if err := receiveWithin(t, leaderDone, "leader request"); err != nil {
		t.Fatalf("leader error=%v", err)
	}
	if _, err := s.info(context.Background(), "example.com/m", "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	if infoCalls.Load() != 1 {
		t.Fatalf("info calls=%d", infoCalls.Load())
	}
}

type infoResult struct {
	info VersionInfo
	err  error
}

//nolint:containedctx // This test wrapper must delegate Context while observing Done calls.
type doneObservingContext struct {
	context.Context

	once     sync.Once
	observed chan struct{}
}

func (c *doneObservingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func receiveWithin[T any](t *testing.T, ch <-chan T, operation string) T {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(2 * time.Second):
		t.Fatalf("%s did not finish", operation)
		var zero T
		return zero
	}
}

func requireOKStatuses(t *testing.T, statuses <-chan int, count int) {
	t.Helper()
	for range count {
		if code := receiveWithin(t, statuses, "concurrent request"); code != http.StatusOK {
			t.Fatalf("status=%d", code)
		}
	}
}

type callsSource struct{ n atomic.Int32 }

func (s *callsSource) AvailableAt(context.Context, string, string, time.Time) (availability.Availability, error) {
	s.n.Add(1)
	return availability.Availability{}, nil
}
func newTestServer(t *testing.T, upstream string, source availability.Source) *Server {
	t.Helper()
	s, err := New(Config{Upstream: upstream, Source: source, Cooldown: 14 * 24 * time.Hour, Now: func() time.Time { return now }, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func fakeProxy(t *testing.T, data map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v, ok := data[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, v)
			return
		}
		http.NotFound(w, r)
	}))
}
func info(v string, tm time.Time) string {
	return fmt.Sprintf(`{"Version":%q,"Time":%q}`, v, tm.Format(time.RFC3339))
}
