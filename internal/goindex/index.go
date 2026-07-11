// Package goindex downloads a complete recent snapshot of index.golang.org.
package goindex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Goryudyuma/gomod-cooldown/internal/availability"
)

const pageLimit = 2000

// Record is one timestamped module-version entry from the index feed.
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

// Snapshot returns every unique module version first cached at or after cutoff.
func (f Fetcher) Snapshot(ctx context.Context, cutoff time.Time) (map[string]time.Time, error) {
	u, client, err := f.client()
	if err != nil {
		return nil, err
	}
	// Start one nanosecond before the cutoff so a record exactly at the boundary
	// cannot be lost if the service treats since as exclusive.
	cursor := cutoff.Add(-time.Nanosecond).UTC()
	recent := make(map[string]time.Time)
	for {
		page, err := fetchPage(ctx, client, u, cursor)
		if err != nil {
			return nil, err
		}
		last, err := addRecent(recent, page, cutoff)
		if err != nil {
			return nil, err
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

func (f Fetcher) client() (*url.URL, *http.Client, error) {
	base := f.BaseURL
	if base == "" {
		base = "https://index.golang.org"
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, nil, fmt.Errorf("invalid index URL %q", base)
	}
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return u, &clientCopy, nil
}

func fetchPage(ctx context.Context, client *http.Client, base *url.URL, cursor time.Time) ([]Record, error) {
	q := url.Values{"since": []string{cursor.Format(time.RFC3339Nano)}, "limit": []string{strconv.Itoa(pageLimit)}}
	reqURL := strings.TrimRight(base.String(), "/") + "/index?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create index request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch index: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("index returned %s", resp.Status)
	}
	return decodePage(resp.Body)
}

func addRecent(recent map[string]time.Time, page []Record, cutoff time.Time) (time.Time, error) {
	var last time.Time
	for _, r := range page {
		if r.Path == "" || r.Version == "" || r.Timestamp.IsZero() {
			return time.Time{}, errors.New("invalid index record")
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
	return last, nil
}

func decodePage(body interface {
	Read(p []byte) (n int, err error)
}) ([]Record, error) {
	s := bufio.NewScanner(body)
	// Index records are small, but do not make a silently small Scanner limit.
	s.Buffer(make([]byte, 4096), 1024*1024)
	var records []Record
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" {
			return nil, errors.New("invalid empty index record")
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
