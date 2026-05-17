package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHumanize(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.00 KiB"},
		{1536, "1.50 KiB"},
		{1 << 20, "1.00 MiB"},
		{1 << 30, "1.00 GiB"},
		{13_249_974_108, "12.34 GiB"},
	}
	for _, tt := range tests {
		if got := Humanize(tt.n); got != tt.want {
			t.Errorf("Humanize(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tavazon.log")
	w := &rotatingWriter{path: path, maxSize: 100, maxBackups: 2}
	if err := w.open(); err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	line := []byte(strings.Repeat("x", 40) + "\n") // 41 bytes
	for i := 0; i < 10; i++ {
		if _, err := w.Write(line); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	// 10 x 41 = 410 bytes through a 100-byte cap must have rotated.
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("expected a rotated backup %s.1: %v", path, err)
	}
	// Only maxBackups (2) generations are kept.
	if _, err := os.Stat(path + ".3"); err == nil {
		t.Errorf("%s.3 should not exist with max_backups=2", path)
	}
}
