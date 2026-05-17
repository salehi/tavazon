// Package state holds the persistent synchronizer offsets and the TTL IP cache,
// saved atomically to a local JSON file. See docs/project.md §7.5, §9.1.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// CacheEntry is one cached destination in the TTL IP cache.
type CacheEntry struct {
	Port    int       `json:"port"`
	ASN     uint32    `json:"asn"`
	Expires time.Time `json:"expires"`
}

// State is the persistent runtime state. It is written atomically by Save.
// The exported fields carry the JSON; concurrent mutation must hold mu, which
// Save and PurgeExpired take internally.
type State struct {
	mu sync.Mutex

	TotalUpload     int64                 `json:"total_upload"`
	TotalDownload   int64                 `json:"total_download"`
	UploadSync      int64                 `json:"upload_sync"`
	DownloadSync    int64                 `json:"download_sync"`
	WindowStart     time.Time             `json:"window_start"`
	WindowSentBytes int64                 `json:"window_sent_bytes"`
	IPCache         map[string]CacheEntry `json:"ip_cache"`
	UpdatedAt       time.Time             `json:"updated_at"`

	path string
}

// Load reads state from path. A missing file is not an error: a zero-valued
// state bound to path is returned (the first-run case).
func Load(path string) (*State, error) {
	s := &State{path: path, IPCache: make(map[string]CacheEntry)}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("state: read %q: %w", path, err)
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("state: parse %q: %w", path, err)
	}
	if s.IPCache == nil {
		s.IPCache = make(map[string]CacheEntry)
	}
	s.path = path
	return s, nil
}

// Save writes the state atomically: marshal, write a temp file, fsync, then
// rename over the live file. A crash mid-write never corrupts the live file.
func (s *State) Save() error {
	s.mu.Lock()
	s.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(s, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("state: marshal: %w", err)
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("state: create %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("state: write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("state: fsync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("state: close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("state: rename %s: %w", tmp, err)
	}
	return nil
}

// PurgeExpired drops IP-cache entries whose expiry is at or before now.
func (s *State) PurgeExpired(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ip, e := range s.IPCache {
		if !e.Expires.After(now) {
			delete(s.IPCache, ip)
		}
	}
}
