// Package engine is the orchestration loop that ties every other package
// together. Once per cycle it reads the kernel network counters, re-anchors the
// synchronizer across reboots, computes the traffic deficit, drives the
// uploader, and samples the metering and metrics stores. It owns no UI and
// exposes no Pause/Resume — the run state lives in config. See docs/project.md
// §6.11, §7.10.
package engine

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"sync"
	"time"

	"tavazon/internal/config"
	"tavazon/internal/logging"
	"tavazon/internal/metering"
	"tavazon/internal/metrics"
	"tavazon/internal/netstat"
	"tavazon/internal/schedule"
	"tavazon/internal/state"
	"tavazon/internal/sysstat"
)

// bytesPerMbit converts a link speed in Mbit/s to bytes/s (1 Mbit = 1e6 bit).
const bytesPerMbit = 125000

// stoppedPollInterval is how long the loop sleeps between checks of
// general.running while the service is Stopped from the dashboard. It is a var
// so tests can shorten it.
var stoppedPollInterval = time.Second

// samplingInterval is how often the sampler refreshes the dashboard's live
// speed and resource gauges, independent of the traffic cycle. It is a var so
// tests can shorten it.
var samplingInterval = 2 * time.Second

// cycleRunner is the slice of the uploader the engine depends on.
// *uploader.Uploader satisfies it; tests substitute a stub.
type cycleRunner interface {
	RunCycle(ctx context.Context, budget int64, workers int) int64
}

// Engine is the main orchestration loop. It is driven from a single goroutine
// (docs/project.md §13): it reads Config through a closure (the web layer
// rewrites it under a lock), owns state mutation, and leaves metrics/metering
// — which guard themselves — to be read concurrently by the web layer.
type Engine struct {
	cfg      func() *config.Config // always the current (reloadable) config
	state    *state.State
	netstat  func(iface string) (netstat.Counters, error) // injectable for tests
	sysstat  func() (sysstat.Sample, error)               // injectable for tests
	sched    *schedule.Curve
	uploader cycleRunner
	metering *metering.Store
	metrics  *metrics.Registry
	log      *slog.Logger
	rng      *rand.Rand

	prevSys     sysstat.Sample
	prevWorkers int
}

// New assembles an Engine from its wired dependencies. The netstat and sysstat
// sources default to the real /proc readers; tests overwrite the fields.
func New(
	cfg func() *config.Config,
	st *state.State,
	curve *schedule.Curve,
	up cycleRunner,
	store *metering.Store,
	reg *metrics.Registry,
	log *slog.Logger,
	rng *rand.Rand,
) *Engine {
	return &Engine{
		cfg:      cfg,
		state:    st,
		netstat:  netstat.Read,
		sysstat:  sysstat.Read,
		sched:    curve,
		uploader: up,
		metering: store,
		metrics:  reg,
		log:      log,
		rng:      rng,
	}
}

// Run is the engine loop. It runs until ctx is cancelled, then writes a final
// state snapshot and returns nil. While general.running is false (Stopped via
// the dashboard) the loop idles, polling the flag; it picks the change up live,
// with no restart and no method call. Every cycle body runs under recover() so
// a single panicking cycle logs a stack trace and the loop continues — the
// daemon never dies from one bad cycle (flaw #3).
func (e *Engine) Run(ctx context.Context) error {
	e.log.Info("service started")
	e.state.PurgeExpired(time.Now())

	// The sampler refreshes the dashboard's live speed and resource gauges on a
	// fixed cadence, decoupled from the traffic cycle: a single volume-mode
	// cycle can run for hours, and the gauges must stay live throughout.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		e.runSampler(ctx)
	}()

	for ctx.Err() == nil {
		cfg := e.cfg()
		if !cfg.General.Running {
			e.sleep(ctx, stoppedPollInterval)
			continue
		}
		e.cycle(ctx)
		e.sleep(ctx, randomDuration(
			cfg.General.IntervalMin.Std(), cfg.General.IntervalMax.Std(), e.rng))
	}

	wg.Wait()
	e.log.Info("service stopping")
	if err := e.state.Save(); err != nil {
		e.log.Error("final state save failed", "err", err)
	}
	return nil
}

// cycle runs one iteration of runCycle under recover() so a panic never
// escapes the loop.
func (e *Engine) cycle(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("cycle panic recovered",
				"panic", r, "stack", string(debug.Stack()))
		}
	}()
	e.runCycle(ctx)
}

// runCycle performs one full cycle: read counters, reconcile the synchronizer,
// compute the deficit, optionally manufacture traffic, then sample the stores
// and persist state.
func (e *Engine) runCycle(ctx context.Context) {
	cfg := e.cfg()
	now := time.Now()
	start := now
	iface := cfg.Network.Interface

	raw, err := e.netstat(iface)
	if err != nil {
		e.log.Error("netstat read failed", "iface", iface, "err", err)
		return
	}
	e.reconcile(raw)

	deficit := e.deficit(cfg, now, e.state.TotalUpload, e.state.TotalDownload)
	e.metrics.SetDeficit(deficit)

	intensity := e.sched.Intensity(now)
	e.metrics.SetCurveIntensity(intensity)

	var sent int64
	workers := 0
	if deficit > 0 && (len(cfg.GeoIP.SelectedASNs) > 0 || cfg.Dev.Enabled) {
		workers = e.workerCount(cfg, intensity)
		if workers > 0 {
			budget := int64(float64(deficit) * cfg.Uploader.CycleBudgetFraction)
			sent = e.uploader.RunCycle(ctx, budget, workers)
			e.prevWorkers = workers
			// Re-read so the tracked totals reflect the just-sent traffic
			// immediately (docs/project.md §6.11).
			if raw2, err := e.netstat(iface); err == nil {
				e.reconcile(raw2)
			} else {
				e.log.Error("netstat re-read failed", "iface", iface, "err", err)
			}
			e.log.Info("cycle complete",
				"fake_bytes", logging.Humanize(sent),
				"fake_bytes_raw", sent,
				"workers", workers,
				"duration", time.Since(start).Round(time.Millisecond).String())
		}
	}
	e.metrics.SetWorkers(workers)

	if cfg.Target.Mode == "volume" {
		e.state.WindowSentBytes += sent
	}

	if err := e.metering.Sample(now, e.state.TotalUpload, e.state.TotalDownload); err != nil {
		e.log.Error("metering sample failed", "err", err)
	}
	e.metrics.ObserveTracked(e.state.TotalUpload, e.state.TotalDownload)

	if err := e.state.Save(); err != nil {
		e.log.Error("state save failed", "err", err)
	}
}

// reconcile applies the §6.2 synchronizer logic. The tracked lifetime totals
// are rawCounter + sync; when either raw counter drops below its tracked total
// the machine rebooted (or the counter wrapped), so the synchronizer is
// re-anchored to keep the tracked total continuous. The improvement over the
// original is triggering on *either* counter, not both (flaw #7).
func (e *Engine) reconcile(raw netstat.Counters) {
	liveUp := int64(raw.TxBytes) + e.state.UploadSync
	liveDown := int64(raw.RxBytes) + e.state.DownloadSync
	if liveUp < e.state.TotalUpload || liveDown < e.state.TotalDownload {
		e.state.UploadSync = e.state.TotalUpload - int64(raw.TxBytes)
		e.state.DownloadSync = e.state.TotalDownload - int64(raw.RxBytes)
		e.log.Warn("reboot detected, synchronizer re-anchored",
			"raw_tx", raw.TxBytes, "raw_rx", raw.RxBytes,
			"tracked_up", e.state.TotalUpload, "tracked_down", e.state.TotalDownload)
		return
	}
	e.state.TotalUpload = liveUp
	e.state.TotalDownload = liveDown
}

// deficit computes how many bytes of fake upload are owed right now under the
// active target mode (docs/project.md §6.3). Volume mode rolls the window over
// here — VolumeDeficit itself is pure.
func (e *Engine) deficit(cfg *config.Config, now time.Time, trackedUp, trackedDown int64) int64 {
	if cfg.Target.Mode == "volume" {
		e.rollWindow(cfg, now)
		return schedule.VolumeDeficit(now, e.state.WindowStart,
			e.state.WindowSentBytes, cfg.Target.Volume, e.sched)
	}
	return schedule.RatioDeficit(trackedUp, trackedDown, cfg.Target.Ratio, e.rng)
}

// rollWindow advances the volume-mode window to the one containing now,
// resetting the per-window sent counter on each roll.
func (e *Engine) rollWindow(cfg *config.Config, now time.Time) {
	window := cfg.Target.Volume.Window.Std()
	if window <= 0 {
		return
	}
	if e.state.WindowStart.IsZero() {
		e.state.WindowStart = now
		e.state.WindowSentBytes = 0
		return
	}
	for now.Sub(e.state.WindowStart) >= window {
		e.state.WindowStart = e.state.WindowStart.Add(window)
		e.state.WindowSentBytes = 0
	}
}

// workerCount returns the worker count for this cycle. In dev mode it is the
// fixed dev.workers (no curve); otherwise it is the curve-derived target,
// slew-limited so cycle-to-cycle change is smooth and capped at MaxWorkers
// (docs/project.md §6.4). The result is clamped to [0, MaxWorkers]; a zero
// non-dev count means the curve trough is idle and the cycle uploads nothing.
func (e *Engine) workerCount(cfg *config.Config, intensity float64) int {
	uc := cfg.Uploader
	if cfg.Dev.Enabled {
		return clamp(cfg.Dev.Workers, 1, uc.MaxWorkers)
	}
	base := uc.ThreadsCoefficient * 10
	target := int(intensity * float64(base))
	return clamp(slew(e.prevWorkers, target, uc.MaxRamp), 0, uc.MaxWorkers)
}

// runSampler refreshes the dashboard's live metrics — upload/download speed
// (from the raw counters of the *configured* interface) and the system resource
// gauges — on a fixed cadence, independent of the traffic cycle. It reads only
// /proc and the concurrency-safe metrics registry and never mutates engine
// state, so it runs safely alongside the single state-owning cycle goroutine.
// Sampling here (not in the cycle) keeps the chart and gauges live even when a
// single volume-mode cycle runs for hours.
func (e *Engine) runSampler(ctx context.Context) {
	t := time.NewTicker(samplingInterval)
	defer t.Stop()
	for {
		e.sample()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// sample takes one live-metrics reading under recover() so a bad read never
// kills the sampler goroutine. Speed is measured on the configured interface
// (cfg.Network.Interface) — the same one selected in the dashboard — so an
// empty value ("All") sums non-loopback NICs while a named value isolates it.
func (e *Engine) sample() {
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("sampler panic recovered",
				"panic", r, "stack", string(debug.Stack()))
		}
	}()
	cfg := e.cfg()
	if raw, err := e.netstat(cfg.Network.Interface); err != nil {
		e.log.Debug("sampler netstat read failed",
			"iface", cfg.Network.Interface, "err", err)
	} else {
		e.metrics.ObserveSpeed(time.Now(), raw.TxBytes, raw.RxBytes)
	}
	e.sampleResources(cfg)
}

// sampleResources records whole-machine CPU/RAM and the upload bandwidth use
// for the dashboard resource panel (docs/project.md §7.11a). It is best-effort
// and independent of the network counters: a /proc read failure is logged at
// debug level and the panel keeps its last values.
func (e *Engine) sampleResources(cfg *config.Config) {
	cur, err := e.sysstat()
	if err != nil {
		e.log.Debug("sysstat read failed", "err", err)
		return
	}
	var res metrics.Resources
	if !e.prevSys.At.IsZero() {
		res.CPUPct = sysstat.CPUPercent(e.prevSys, cur)
	}
	e.prevSys = cur
	res.RAMUsedBytes = cur.MemUsedBytes
	res.RAMTotalBytes = cur.MemTotalBytes
	if cur.MemTotalBytes > 0 {
		res.RAMPct = float64(cur.MemUsedBytes) / float64(cur.MemTotalBytes) * 100
	}
	res.BandwidthBPS = e.metrics.Snapshot().UploadBPS
	if cfg.Network.LinkCapacityMbit > 0 {
		res.LinkCapacityBPS = int64(cfg.Network.LinkCapacityMbit) * bytesPerMbit
		res.BandwidthPct = float64(res.BandwidthBPS) / float64(res.LinkCapacityBPS) * 100
	}
	e.metrics.SetResources(res)
}

// sleep blocks for d, returning early if ctx is cancelled.
func (e *Engine) sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// randomDuration returns a uniform random duration in [min, max].
func randomDuration(min, max time.Duration, rng *rand.Rand) time.Duration {
	if max <= min {
		return min
	}
	return min + time.Duration(rng.Int64N(int64(max-min)+1))
}

// slew clamps the step from prev toward target to at most ±maxRamp.
func slew(prev, target, maxRamp int) int {
	if maxRamp < 1 {
		maxRamp = 1
	}
	switch d := target - prev; {
	case d > maxRamp:
		return prev + maxRamp
	case d < -maxRamp:
		return prev - maxRamp
	default:
		return target
	}
}

// clamp constrains v to [lo, hi].
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
