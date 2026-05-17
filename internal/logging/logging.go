// Package logging configures the slog logger and a size-rotating file writer.
// See docs/project.md §7.12, §11.2.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/salehi/tavazon/internal/config"
)

// rotatingWriter is an io.Writer that keeps its file open and rotates it once
// it exceeds maxSize, keeping maxBackups generations. It is concurrency-safe:
// slog may write from many goroutines.
type rotatingWriter struct {
	mu         sync.Mutex
	path       string
	maxSize    int64
	maxBackups int
	f          *os.File
	size       int64
}

func newRotatingWriter(path string, maxSizeMB, maxBackups int) (*rotatingWriter, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("logging: create log dir: %w", err)
		}
	}
	w := &rotatingWriter{
		path:       path,
		maxSize:    int64(maxSizeMB) * 1024 * 1024,
		maxBackups: maxBackups,
	}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("logging: open %s: %w", w.path, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("logging: stat %s: %w", w.path, err)
	}
	w.f = f
	w.size = info.Size()
	return nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.maxSize > 0 && w.size > 0 && w.size+int64(len(p)) > w.maxSize {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingWriter) rotate() error {
	if err := w.f.Close(); err != nil {
		return fmt.Errorf("logging: close during rotate: %w", err)
	}
	if w.maxBackups < 1 {
		// Keep no backups: truncate and reopen.
		f, err := os.OpenFile(w.path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return fmt.Errorf("logging: truncate %s: %w", w.path, err)
		}
		w.f, w.size = f, 0
		return nil
	}
	// Shift backups: drop the oldest, then path.(n-1) -> path.n ... path -> path.1.
	// Backup renames are best-effort; a missing generation is benign.
	os.Remove(w.backupName(w.maxBackups))
	for i := w.maxBackups; i >= 2; i-- {
		os.Rename(w.backupName(i-1), w.backupName(i))
	}
	if err := os.Rename(w.path, w.backupName(1)); err != nil {
		return fmt.Errorf("logging: rotate %s: %w", w.path, err)
	}
	return w.open()
}

func (w *rotatingWriter) backupName(i int) string {
	return fmt.Sprintf("%s.%d", w.path, i)
}

// Close closes the underlying file.
func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f != nil {
		return w.f.Close()
	}
	return nil
}

// Setup builds an slog logger that writes JSON to a size-rotating file and
// mirrors it to stderr. The returned function closes the log file.
func Setup(cfg config.LogConfig) (*slog.Logger, func() error, error) {
	rw, err := newRotatingWriter(cfg.File, cfg.MaxSizeMB, cfg.MaxBackups)
	if err != nil {
		return nil, nil, err
	}
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	h := slog.NewJSONHandler(io.MultiWriter(rw, os.Stderr), &slog.HandlerOptions{Level: level})
	return slog.New(h), rw.Close, nil
}

// Humanize formats a byte count as a human-readable IEC string ("12.34 GiB").
func Humanize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	f := float64(n)
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	i := 0
	f /= unit
	for f >= unit && i < len(units)-1 {
		f /= unit
		i++
	}
	return fmt.Sprintf("%.2f %s", f, units[i])
}
