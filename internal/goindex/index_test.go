package goindex

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Goryudyuma/gomod-cooldown/internal/availability"
)

func TestSnapshotPagesDuplicatesAndBoundary(t *testing.T) {
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			for i := 0; i < pageLimit; i++ {
				fmt.Fprintf(w, `{"Path":"m%d","Version":"v1.0.0","Timestamp":%q}`+"\n", i, cutoff.Add(time.Duration(i+1)*time.Nanosecond).Format(time.RFC3339Nano))
			}
			return
		}
		fmt.Fprintf(w, `{"Path":"m0","Version":"v1.0.0","Timestamp":%q}`+"\n"+`{"Path":"edge","Version":"v1.0.0","Timestamp":%q}`+"\n", cutoff.Add(time.Nanosecond).Format(time.RFC3339Nano), cutoff.Format(time.RFC3339Nano))
	}))
	defer s.Close()
	m, err := (Fetcher{BaseURL: s.URL, Client: s.Client()}).Snapshot(context.Background(), cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(m) != 2001 || !m[availability.Key("edge", "v1.0.0")].Equal(cutoff) {
		t.Fatalf("calls=%d len=%d", calls, len(m))
	}
}
func TestSnapshotFailsClosed(t *testing.T) {
	cutoff := time.Now().Add(-time.Hour)
	for name, handler := range map[string]http.HandlerFunc{
		"malformed": func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "not json\n") },
		"http":      func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 500) },
		"stuck": func(w http.ResponseWriter, r *http.Request) {
			for i := 0; i < pageLimit; i++ {
				fmt.Fprintf(w, `{"Path":"m","Version":"v1.0.0","Timestamp":%q}`+"\n", cutoff.Format(time.RFC3339Nano))
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := httptest.NewServer(handler)
			defer s.Close()
			if _, err := (Fetcher{BaseURL: s.URL, Client: s.Client()}).Snapshot(context.Background(), cutoff); err == nil {
				t.Fatal("wanted error")
			}
		})
	}
}

func TestSnapshotTimeout(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(50 * time.Millisecond) }))
	defer s.Close()
	client := s.Client()
	client.Timeout = time.Millisecond
	if _, err := (Fetcher{BaseURL: s.URL, Client: client}).Snapshot(context.Background(), time.Now().Add(-time.Hour)); err == nil {
		t.Fatal("wanted timeout error")
	}
}

func TestSnapshotDoesNotFollowRedirect(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer target.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer s.Close()
	if _, err := (Fetcher{BaseURL: s.URL, Client: s.Client()}).Snapshot(context.Background(), time.Now().Add(-time.Hour)); err == nil {
		t.Fatal("wanted redirect error")
	}
	if redirected.Load() != 0 {
		t.Fatal("redirect target was contacted")
	}
}
func TestDecodePageRejectsBlank(t *testing.T) {
	if _, err := decodePage(strings.NewReader("\n")); err == nil {
		t.Fatal("wanted error")
	}
}

func TestSnapshotForCooldownUsesInjectedClock(t *testing.T) {
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer s.Close()
	_, cutoff, err := (Fetcher{BaseURL: s.URL, Client: s.Client(), Now: func() time.Time { return now }}).SnapshotForCooldown(context.Background(), 7*24*time.Hour)
	if err != nil || !cutoff.Equal(now.Add(-7*24*time.Hour)) {
		t.Fatalf("cutoff=%s err=%v", cutoff, err)
	}
}
