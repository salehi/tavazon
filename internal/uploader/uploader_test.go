package uploader

import (
	"context"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"testing"
	"time"

	"github.com/salehi/tavazon/internal/config"
	"github.com/salehi/tavazon/internal/geoip"
	"github.com/salehi/tavazon/internal/metering"
	"github.com/salehi/tavazon/internal/metrics"
	"github.com/salehi/tavazon/internal/targets"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func mustCIDR(t *testing.T, s string) net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("bad CIDR %q: %v", s, err)
	}
	return *n
}

func TestRandomSizeBounds(t *testing.T) {
	cfg := config.UploaderConfig{MinDatagram: 64, MaxDatagram: 1472}
	rng := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 100000; i++ {
		s := RandomSize(cfg, rng)
		if s < cfg.MinDatagram || s > cfg.MaxDatagram {
			t.Fatalf("RandomSize = %d, outside [%d,%d]", s, cfg.MinDatagram, cfg.MaxDatagram)
		}
	}
}

func TestRandomNonConstant(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	buf := make([]byte, 1000)
	Random(buf, rng)
	for _, b := range buf {
		if b != buf[0] {
			return // varied — good
		}
	}
	t.Error("Random produced a constant buffer")
}

func TestTokenBucketLimits(t *testing.T) {
	const rate = 100000.0 // bytes/sec
	b := newTokenBucket(rate)
	ctx := context.Background()
	start := time.Now()
	var got int
	for time.Since(start) < 600*time.Millisecond {
		if err := b.WaitN(ctx, 1000); err != nil {
			t.Fatal(err)
		}
		got += 1000
	}
	effective := float64(got) / time.Since(start).Seconds()
	// Without limiting this loop would deliver megabytes; the bucket holds it
	// to roughly the configured rate (plus the initial one-second burst).
	if effective < rate*0.5 || effective > rate*4 {
		t.Errorf("effective rate %.0f B/s, want roughly %.0f", effective, rate)
	}
}

func devConfig() *config.Config {
	c := config.Default()
	c.Targets = config.TargetsConfig{PortMin: 20000, PortMax: 30000}
	c.Uploader.PacketGapMax = 0 // no inter-packet sleep — keep the test fast
	return c
}

func TestRunCycleSendsAndMeters(t *testing.T) {
	g := geoip.NewForTest(
		map[uint32][]net.IPNet{4242: {mustCIDR(t, "127.0.0.0/8")}},
		map[uint32]string{4242: "IR"},
	)
	tg, _, err := targets.New(g, []uint32{4242}, config.TargetsConfig{PortMin: 20000, PortMax: 30000}, rand.New(rand.NewPCG(1, 2)))
	if err != nil {
		t.Fatalf("targets.New: %v", err)
	}
	store, err := metering.Open(config.MeteringConfig{Dir: t.TempDir(), Retention5Min: config.Duration(9000 * time.Hour)})
	if err != nil {
		t.Fatalf("metering.Open: %v", err)
	}
	reg := metrics.New()
	u := New(tg, reg, store, devConfig, discardLogger(), rand.New(rand.NewPCG(9, 9)))

	sent := u.RunCycle(context.Background(), 200000, 8)
	if sent <= 0 {
		t.Fatalf("RunCycle sent %d bytes, want > 0", sent)
	}
	if reg.Snapshot().FakeBytesTotal <= 0 {
		t.Error("metrics.FakeBytesTotal did not advance")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	hist, err := store.History(time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var asnBytes int64
	for _, b := range hist {
		asnBytes += b.PerASN[4242]
	}
	if asnBytes <= 0 {
		t.Error("metering recorded no bytes for ASN 4242")
	}
}

func TestRunCycleRespectsCancellation(t *testing.T) {
	g := geoip.NewForTest(
		map[uint32][]net.IPNet{4242: {mustCIDR(t, "127.0.0.0/8")}},
		map[uint32]string{4242: "IR"},
	)
	tg, _, _ := targets.New(g, []uint32{4242}, config.TargetsConfig{PortMin: 20000, PortMax: 30000}, rand.New(rand.NewPCG(1, 2)))
	store, _ := metering.Open(config.MeteringConfig{Dir: t.TempDir(), Retention5Min: config.Duration(time.Hour)})
	u := New(tg, metrics.New(), store, devConfig, discardLogger(), rand.New(rand.NewPCG(9, 9)))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	done := make(chan struct{})
	go func() {
		u.RunCycle(ctx, 1<<40, 16) // an enormous budget must still return promptly
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunCycle did not return after ctx cancellation")
	}
}
