// Package metering is the long-horizon time-series store: per-ASN accounting,
// 95th-percentile billing, and the config-change audit log.
// See docs/project.md §6.10, §7.9.
package metering

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/salehi/tavazon/internal/config"
)

// bucketDuration is the 5-minute sampling window used for billing.
const bucketDuration = 5 * time.Minute

// Bucket is one 5-minute sample of transferred volume.
type Bucket struct {
	Start     time.Time        `json:"t"`
	UpBytes   int64            `json:"up_bytes"`
	DownBytes int64            `json:"down_bytes"`
	FakeBytes int64            `json:"fake_bytes"`
	PerASN    map[uint32]int64 `json:"per_asn"`
}

func newBucket(start time.Time) *Bucket {
	return &Bucket{Start: start, PerASN: make(map[uint32]int64)}
}

// Store is the append-only metering store rooted at a local directory.
type Store struct {
	dir       string
	retention time.Duration

	mu       sync.Mutex
	cur      *Bucket
	lastUp   int64
	lastDown int64
	haveLast bool
}

// Open prepares the metering store directory.
func Open(cfg config.MeteringConfig) (*Store, error) {
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("metering: create dir %q: %w", cfg.Dir, err)
	}
	return &Store{dir: cfg.Dir, retention: cfg.Retention5Min.Std()}, nil
}

func bucketStart(t time.Time) time.Time { return t.UTC().Truncate(bucketDuration) }

// Sample records the tracked lifetime counters at time now, accumulating the
// delta since the previous call into the current 5-minute bucket. Crossing a
// window boundary flushes the finished bucket. The first call only sets a
// baseline.
func (s *Store) Sample(now time.Time, trackedUp, trackedDown int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.rollTo(now); err != nil {
		return err
	}
	if s.haveLast {
		if d := trackedUp - s.lastUp; d > 0 {
			s.cur.UpBytes += d
		}
		if d := trackedDown - s.lastDown; d > 0 {
			s.cur.DownBytes += d
		}
	}
	s.lastUp, s.lastDown, s.haveLast = trackedUp, trackedDown, true
	return nil
}

// RecordSend attributes n fake bytes sent to asn. It is concurrency-safe, so
// upload workers may call it directly.
func (s *Store) RecordSend(asn uint32, n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		s.cur = newBucket(bucketStart(time.Now()))
	}
	s.cur.FakeBytes += n
	s.cur.PerASN[asn] += n
}

// rollTo advances the current bucket to the window containing now, flushing
// any finished bucket. The caller holds s.mu.
func (s *Store) rollTo(now time.Time) error {
	bs := bucketStart(now)
	switch {
	case s.cur == nil:
		s.cur = newBucket(bs)
	case !s.cur.Start.Equal(bs):
		if err := s.flush(s.cur); err != nil {
			return err
		}
		s.cur = newBucket(bs)
	}
	return nil
}

func (s *Store) monthlyPath(t time.Time) string {
	return filepath.Join(s.dir, t.UTC().Format("2006-01")+".5min.jsonl")
}

func (s *Store) flush(b *Bucket) error {
	line, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("metering: marshal bucket: %w", err)
	}
	path := s.monthlyPath(b.Start)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("metering: open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("metering: write %s: %w", path, err)
	}
	return nil
}

// Close flushes the in-progress bucket.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return nil
	}
	err := s.flush(s.cur)
	s.cur = nil
	return err
}

// History returns the 5-minute buckets recorded in [from, to], oldest first,
// including any in-progress (unflushed) bucket.
func (s *Store) History(from, to time.Time) ([]Bucket, error) {
	s.mu.Lock()
	cur := s.cur
	s.mu.Unlock()

	var out []Bucket
	month := time.Date(from.UTC().Year(), from.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	for ; !month.After(to.UTC()); month = month.AddDate(0, 1, 0) {
		buckets, err := readBuckets(s.monthlyPath(month))
		if err != nil {
			return nil, err
		}
		for _, b := range buckets {
			if !b.Start.Before(from) && !b.Start.After(to) {
				out = append(out, b)
			}
		}
	}
	if cur != nil && !cur.Start.Before(from) && !cur.Start.After(to) {
		out = append(out, *cur)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

func readBuckets(path string) ([]Bucket, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("metering: open %s: %w", path, err)
	}
	defer f.Close()
	var out []Bucket
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var b Bucket
		if err := json.Unmarshal(sc.Bytes(), &b); err != nil {
			return nil, fmt.Errorf("metering: parse %s: %w", path, err)
		}
		out = append(out, b)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("metering: scan %s: %w", path, err)
	}
	return out, nil
}
