// Package goindex downloads a complete recent snapshot of index.golang.org.
package goindex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Goryudyuma/gomod-cooldown/internal/availability"
)

const pageLimit = 2000

type Record struct {
	Path      string
	Version   string
	Timestamp time.Time
}

// Fetcher reads the chronological NDJSON feed. Index's documented ordering is
// essential: when a page has fewer than limit records, the snapshot is complete.
type Fetcher struct {
	BaseURL string
	Client  *http.Client
	Now     func() time.Time
}

// SnapshotForCooldown derives a cutoff from the injectable clock and returns
// the exact cutoff used. Callers should use that same cutoff clock for later
// decisions so the snapshot and proxy cannot drift apart.
func (f Fetcher) SnapshotForCooldown(ctx context.Context, cooldown time.Duration) (map[string]time.Time, time.Time, error) {
	now := f.Now
	if now == nil {
		now = time.Now
	}
	cutoff := now().Add(-cooldown)
	recent, err := f.Snapshot(ctx, cutoff)
	return recent, cutoff, err
}

func (f Fetcher) Snapshot(ctx context.Context, cutoff time.Time) (map[string]time.Time, error) {
	base := f.BaseURL
	if base == "" {
		base = "https://index.golang.org"
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("invalid index URL %q", base)
	}
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	client = &clientCopy
	// Start one nanosecond before the cutoff so a record exactly at the boundary
	// cannot be lost if the service treats since as exclusive.
	cursor := cutoff.Add(-time.Nanosecond).UTC()
	recent := make(map[string]time.Time)
	for {
		q := url.Values{"since": []string{cursor.Format(time.RFC3339Nano)}, "limit": []string{fmt.Sprint(pageLimit)}}
		reqURL := strings.TrimRight(u.String(), "/") + "/index?" + q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, fmt.Errorf("create index request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch index: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("index returned %s", resp.Status)
		}
		page, err := decodePage(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		var last time.Time
		for _, r := range page {
			if r.Path == "" || r.Version == "" || r.Timestamp.IsZero() {
				return nil, fmt.Errorf("invalid index record")
			}
			if r.Timestamp.Before(cutoff) {
				continue
			}
			key := availability.Key(r.Path, r.Version)
			if old, ok := recent[key]; !ok || r.Timestamp.Before(old) {
				recent[key] = r.Timestamp
			}
			last = r.Timestamp
		}
		if len(page) < pageLimit {
			return recent, nil
		}
		if last.IsZero() || !last.After(cursor) {
			return nil, fmt.Errorf("index cursor did not advance from %s", cursor.Format(time.RFC3339Nano))
		}
		cursor = last
	}
}

func decodePage(body interface{ Read([]byte) (int, error) }) ([]Record, error) {
	s := bufio.NewScanner(body)
	// Index records are small, but do not make a silently small Scanner limit.
	s.Buffer(make([]byte, 4096), 1024*1024)
	var records []Record
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" {
			return nil, fmt.Errorf("invalid empty index record")
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("decode index record: %w", err)
		}
		records = append(records, r)
	}
	if err := s.Err(); err != nil {
		return nil, fmt.Errorf("read index response: %w", err)
	}
	return records, nil
}
