package uploader

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"tavazon/internal/config"
)

// RandomSize returns a uniformly random datagram size in
// [MinDatagram, MaxDatagram]. There are no preferred sizes (docs/project.md
// §6.9): a uniform spread has no modal fingerprint for DPI to match.
func RandomSize(cfg config.UploaderConfig, rng *rand.Rand) int {
	span := cfg.MaxDatagram - cfg.MinDatagram
	if span <= 0 {
		return cfg.MaxDatagram
	}
	return cfg.MinDatagram + rng.IntN(span+1)
}

// Random fills buf with fresh pseudo-random bytes. It uses math/rand/v2 for
// speed — the payload is junk, not a secret.
func Random(buf []byte, rng *rand.Rand) {
	i := 0
	for ; i+8 <= len(buf); i += 8 {
		v := rng.Uint64()
		buf[i] = byte(v)
		buf[i+1] = byte(v >> 8)
		buf[i+2] = byte(v >> 16)
		buf[i+3] = byte(v >> 24)
		buf[i+4] = byte(v >> 32)
		buf[i+5] = byte(v >> 40)
		buf[i+6] = byte(v >> 48)
		buf[i+7] = byte(v >> 56)
	}
	if i < len(buf) {
		v := rng.Uint64()
		for ; i < len(buf); i++ {
			buf[i] = byte(v)
			v >>= 8
		}
	}
}

// tokenBucket is a shared byte-rate limiter (docs/project.md §6.8).
type tokenBucket struct {
	mu       sync.Mutex
	rate     float64 // bytes per second
	capacity float64
	tokens   float64
	last     time.Time
}

func newTokenBucket(ratePerSec float64) *tokenBucket {
	if ratePerSec < 1 {
		ratePerSec = 1
	}
	return &tokenBucket{
		rate:     ratePerSec,
		capacity: ratePerSec, // a one-second burst
		tokens:   ratePerSec,
		last:     time.Now(),
	}
}

// WaitN blocks until n tokens are available, or ctx is done.
func (b *tokenBucket) WaitN(ctx context.Context, n int) error {
	for {
		b.mu.Lock()
		now := time.Now()
		b.tokens += now.Sub(b.last).Seconds() * b.rate
		b.last = now
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		if b.tokens >= float64(n) {
			b.tokens -= float64(n)
			b.mu.Unlock()
			return nil
		}
		wait := time.Duration((float64(n) - b.tokens) / b.rate * float64(time.Second))
		b.mu.Unlock()
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
