package engine

import (
	"context"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"tavazon/internal/config"
	"tavazon/internal/metering"
	"tavazon/internal/metrics"
	"tavazon/internal/netstat"
	"tavazon/internal/schedule"
	"tavazon/internal/state"
	"tavazon/internal/sysstat"
)

// TestMain shortens the Stopped poll interval so loop tests run quickly.
func TestMain(m *testing.M) {
	stoppedPollInterval = time.Millisecond
	os.Exit(m.Run())
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// stubUploader is an injectable cycleRunner that records every RunCycle call
// and, optionally, panics to exercise the loop's recover().
type stubUploader struct {
	mu      sync.Mutex
	calls   int
	workers []int
	budgets []int64
	doPanic bool
}

func (s *stubUploader) RunCycle(ctx context.Context, budget int64, workers int) int64 {
	s.mu.Lock()
	s.calls++
	s.workers = append(s.workers, workers)
	s.budgets = append(s.budgets, budget)
	p := s.doPanic
	s.mu.Unlock()
	if p {
		panic("stub uploader panic")
	}
	return budget
}

func (s *stubUploader) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *stubUploader) workerList() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.workers...)
}

// constCounters is a netstat source that always returns the same counters.
func constCounters(tx, rx uint64) func(string) (netstat.Counters, error) {
	return func(string) (netstat.Counters, error) {
		return netstat.Counters{TxBytes: tx, RxBytes: rx}, nil
	}
}

// risingCounters is a netstat source whose counters strictly increase, so no
// reboot is ever detected.
func risingCounters() func(string) (netstat.Counters, error) {
	var mu sync.Mutex
	var n uint64
	return func(string) (netstat.Counters, error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		return netstat.Counters{TxBytes: n * 1000, RxBytes: n * 100}, nil
	}
}

func fakeSysstat() (sysstat.Sample, error) {
	return sysstat.Sample{
		CPUTotal:      1000,
		CPUIdle:       600,
		MemTotalBytes: 1 << 30,
		MemUsedBytes:  1 << 28,
		At:            time.Now(),
	}, nil
}

// liveCfg is a goroutine-safe config holder so tests can flip general.running
// while the engine loop is running.
type liveCfg struct {
	mu sync.Mutex
	c  *config.Config
}

func newLiveCfg() *liveCfg {
	c := config.Default()
	c.General.IntervalMin = config.Duration(time.Millisecond)
	c.General.IntervalMax = config.Duration(time.Millisecond)
	// The 1 GiB default threshold would swallow the tiny test counters; drop
	// it so a small ratio deficit still drives an upload.
	c.Target.Ratio.MinDeficitBytes = 0
	return &liveCfg{c: c}
}

func (l *liveCfg) get() *config.Config {
	l.mu.Lock()
	defer l.mu.Unlock()
	cp := *l.c
	return &cp
}

func (l *liveCfg) setRunning(v bool) {
	l.mu.Lock()
	l.c.General.Running = v
	l.mu.Unlock()
}

// newEngine builds an Engine wired with real (temp-dir-backed) state, metering,
// and metrics, plus the supplied uploader and netstat source.
func newEngine(t *testing.T, lc *liveCfg, up cycleRunner, ns func(string) (netstat.Counters, error)) *Engine {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Load(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("state.Load: %v", err)
	}
	store, err := metering.Open(config.MeteringConfig{
		Dir:           filepath.Join(dir, "metering"),
		Retention5Min: config.Duration(time.Hour),
	})
	if err != nil {
		t.Fatalf("metering.Open: %v", err)
	}
	curve := schedule.NewCurve(lc.c.Curve, rand.New(rand.NewPCG(3, 4)))
	e := New(lc.get, st, curve, up, store, metrics.New(), discardLogger(),
		rand.New(rand.NewPCG(1, 2)))
	e.netstat = ns
	e.sysstat = fakeSysstat
	return e
}

// runFor starts e.Run, lets it run for d, cancels it, and asserts a clean
// return.
func runFor(t *testing.T, e *Engine, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	time.Sleep(d)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

func TestSlew(t *testing.T) {
	cases := []struct {
		prev, target, maxRamp, want int
	}{
		{0, 100, 20, 20},
		{20, 100, 20, 40},
		{90, 100, 20, 100},
		{100, 0, 20, 80},
		{5, 5, 20, 5},
		{0, 100, 0, 1}, // maxRamp floored to 1
	}
	for _, c := range cases {
		if got := slew(c.prev, c.target, c.maxRamp); got != c.want {
			t.Errorf("slew(%d,%d,%d) = %d, want %d",
				c.prev, c.target, c.maxRamp, got, c.want)
		}
	}
}

func TestRandomDuration(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	min, max := 5*time.Second, 30*time.Second
	for i := 0; i < 1000; i++ {
		d := randomDuration(min, max, rng)
		if d < min || d > max {
			t.Fatalf("randomDuration out of range: %v", d)
		}
	}
	if d := randomDuration(max, min, rng); d != max {
		t.Errorf("randomDuration with max<=min = %v, want %v", d, max)
	}
}

// TestReconcileReboot proves the synchronizer keeps the tracked total
// continuous when the kernel TX counter drops on reboot.
func TestReconcileReboot(t *testing.T) {
	st, err := state.Load(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{state: st, log: discardLogger()}

	e.reconcile(netstat.Counters{TxBytes: 1000, RxBytes: 2000})
	if e.state.TotalUpload != 1000 || e.state.TotalDownload != 2000 {
		t.Fatalf("after first cycle: up=%d down=%d, want 1000/2000",
			e.state.TotalUpload, e.state.TotalDownload)
	}

	// Reboot: TX drops, RX holds. The total must not move.
	e.reconcile(netstat.Counters{TxBytes: 500, RxBytes: 2000})
	if e.state.TotalUpload != 1000 || e.state.TotalDownload != 2000 {
		t.Fatalf("after reboot: up=%d down=%d, want 1000/2000 (continuous)",
			e.state.TotalUpload, e.state.TotalDownload)
	}

	// Post-reboot growth continues from the re-anchored offset.
	e.reconcile(netstat.Counters{TxBytes: 600, RxBytes: 2100})
	if e.state.TotalUpload != 1100 || e.state.TotalDownload != 2100 {
		t.Fatalf("after recovery: up=%d down=%d, want 1100/2100",
			e.state.TotalUpload, e.state.TotalDownload)
	}
}

// TestReconcileRebootEitherCounter proves a drop on *only* the RX counter still
// re-anchors — the improvement over the original (flaw #7).
func TestReconcileRebootEitherCounter(t *testing.T) {
	st, err := state.Load(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{state: st, log: discardLogger()}

	e.reconcile(netstat.Counters{TxBytes: 5000, RxBytes: 9000})
	// Only RX drops; TX keeps climbing.
	e.reconcile(netstat.Counters{TxBytes: 5100, RxBytes: 100})
	if e.state.TotalUpload != 5000 || e.state.TotalDownload != 9000 {
		t.Fatalf("rx-only drop did not re-anchor: up=%d down=%d, want 5000/9000",
			e.state.TotalUpload, e.state.TotalDownload)
	}
}

func TestWorkerCountDevMode(t *testing.T) {
	cfg := config.Default()
	cfg.Dev.Enabled = true
	cfg.Dev.Workers = 7
	e := &Engine{}
	if got := e.workerCount(cfg, 1.9); got != 7 {
		t.Errorf("dev-mode workerCount = %d, want 7 (no curve)", got)
	}
	// Dev workers are still capped at MaxWorkers.
	cfg.Dev.Workers = cfg.Uploader.MaxWorkers + 50
	if got := e.workerCount(cfg, 0); got != cfg.Uploader.MaxWorkers {
		t.Errorf("dev workerCount = %d, want cap %d", got, cfg.Uploader.MaxWorkers)
	}
}

// TestWorkerCountSlewLimited proves the production worker count never changes
// by more than MaxRamp between consecutive cycles, ramping up then down.
func TestWorkerCountSlewLimited(t *testing.T) {
	cfg := config.Default()
	e := &Engine{}
	prev := 0
	check := func(intensity float64) {
		w := e.workerCount(cfg, intensity)
		if d := w - prev; d > cfg.Uploader.MaxRamp || d < -cfg.Uploader.MaxRamp {
			t.Fatalf("worker count stepped %d (max ramp %d)", d, cfg.Uploader.MaxRamp)
		}
		prev, e.prevWorkers = w, w
	}
	for i := 0; i < 20; i++ { // ramp up toward a high target
		check(5.0)
	}
	if prev <= 0 {
		t.Fatalf("worker count never ramped up: %d", prev)
	}
	for i := 0; i < 20; i++ { // ramp back down to the idle trough
		check(0)
	}
	if prev != 0 {
		t.Errorf("worker count did not ramp to idle: %d", prev)
	}
}

// TestRunTrackedTotalsAdvance drives several cycles with a rising netstat
// source and asserts the tracked lifetime totals climb.
func TestRunTrackedTotalsAdvance(t *testing.T) {
	lc := newLiveCfg()
	e := newEngine(t, lc, &stubUploader{}, risingCounters())
	runFor(t, e, 60*time.Millisecond)
	if e.state.TotalUpload <= 0 || e.state.TotalDownload <= 0 {
		t.Fatalf("tracked totals did not advance: up=%d down=%d",
			e.state.TotalUpload, e.state.TotalDownload)
	}
}

// TestRunPanicResilience proves a panicking cycle is logged and the loop keeps
// going (flaw #3).
func TestRunPanicResilience(t *testing.T) {
	lc := newLiveCfg()
	lc.c.Dev.Enabled = true // dev mode → upload gate open, deficit positive
	up := &stubUploader{doPanic: true}
	// TX small, RX large → ratio deficit is positive every cycle.
	e := newEngine(t, lc, up, constCounters(100, 1000))
	runFor(t, e, 60*time.Millisecond)
	if up.count() < 2 {
		t.Fatalf("loop did not survive panics: only %d cycles", up.count())
	}
}

// TestRunDeficitGate proves no cycle uploads when the deficit is zero.
func TestRunDeficitGate(t *testing.T) {
	lc := newLiveCfg()
	lc.c.GeoIP.SelectedASNs = []uint32{1} // ASN gate open; only the deficit gates
	up := &stubUploader{}
	// TX hugely exceeds RX×multiplier → ratio deficit is always 0.
	e := newEngine(t, lc, up, constCounters(1_000_000, 100))
	runFor(t, e, 60*time.Millisecond)
	if up.count() != 0 {
		t.Fatalf("uploaded %d times with zero deficit, want 0", up.count())
	}
}

// TestRunDevModeWorkers proves dev mode sizes every cycle from dev.workers and
// uploads (routing to dev.target is the uploader's job).
func TestRunDevModeWorkers(t *testing.T) {
	lc := newLiveCfg()
	lc.c.Dev.Enabled = true
	lc.c.Dev.Workers = 5
	up := &stubUploader{}
	e := newEngine(t, lc, up, constCounters(100, 1000))
	runFor(t, e, 60*time.Millisecond)
	ws := up.workerList()
	if len(ws) == 0 {
		t.Fatal("dev mode never uploaded")
	}
	for i, w := range ws {
		if w != 5 {
			t.Fatalf("cycle %d used %d workers, want dev.workers=5", i, w)
		}
	}
}

// TestRunRestoreState proves an engine loaded with general.running=false starts
// idle — the persisted run state survives a restart.
func TestRunRestoreState(t *testing.T) {
	lc := newLiveCfg()
	lc.c.General.Running = false
	lc.c.Dev.Enabled = true
	up := &stubUploader{}
	e := newEngine(t, lc, up, constCounters(100, 1000))
	runFor(t, e, 40*time.Millisecond)
	if up.count() != 0 {
		t.Fatalf("engine ran %d cycles despite running=false", up.count())
	}
}

// TestRunRunningFlagLive proves the loop reads general.running every cycle:
// flipping it false stops the cycles with no restart, flipping it true resumes
// them.
func TestRunRunningFlagLive(t *testing.T) {
	lc := newLiveCfg()
	lc.c.Dev.Enabled = true
	up := &stubUploader{}
	e := newEngine(t, lc, up, constCounters(100, 1000))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()

	time.Sleep(40 * time.Millisecond)
	if up.count() == 0 {
		t.Fatal("engine never uploaded while running")
	}

	lc.setRunning(false)
	time.Sleep(40 * time.Millisecond) // let any in-flight cycle drain
	stopped := up.count()
	time.Sleep(40 * time.Millisecond) // a full window with running=false
	if up.count() != stopped {
		t.Fatalf("cycles ran while stopped: %d -> %d", stopped, up.count())
	}

	lc.setRunning(true)
	time.Sleep(40 * time.Millisecond)
	if up.count() == stopped {
		t.Fatalf("engine did not resume after running flipped true (still %d)", stopped)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
