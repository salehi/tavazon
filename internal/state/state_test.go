package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load (first run): %v", err)
	}
	s.TotalUpload = 123456789
	s.TotalDownload = 987654321
	s.UploadSync = 1000
	s.DownloadSync = -2000
	s.WindowSentBytes = 555
	s.IPCache["5.1.2.3"] = CacheEntry{Port: 27015, ASN: 12345, Expires: time.Now().Add(time.Hour)}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load (reload): %v", err)
	}
	if got.TotalUpload != 123456789 || got.TotalDownload != 987654321 ||
		got.UploadSync != 1000 || got.DownloadSync != -2000 || got.WindowSentBytes != 555 {
		t.Errorf("scalar fields not preserved: %+v", got)
	}
	e, ok := got.IPCache["5.1.2.3"]
	if !ok || e.Port != 27015 || e.ASN != 12345 {
		t.Errorf("ip-cache entry not preserved: %+v ok=%v", e, ok)
	}
}

func TestSaveIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Load(path)
	s.TotalUpload = 42
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file %s.tmp should not remain after Save", path)
	}
	if _, err := Load(path); err != nil {
		t.Errorf("the saved file is not loadable: %v", err)
	}
}

func TestPurgeExpired(t *testing.T) {
	s, _ := Load(filepath.Join(t.TempDir(), "state.json"))
	now := time.Now()
	s.IPCache["fresh"] = CacheEntry{Expires: now.Add(time.Hour)}
	s.IPCache["stale"] = CacheEntry{Expires: now.Add(-time.Hour)}
	s.PurgeExpired(now)
	if _, ok := s.IPCache["fresh"]; !ok {
		t.Error("a fresh entry was purged")
	}
	if _, ok := s.IPCache["stale"]; ok {
		t.Error("a stale entry was not purged")
	}
}
