// Package metrics holds in-memory counters and the snapshot served to the
// dashboard and the Prometheus endpoint. See docs/project.md §7.11, §11.1.
package metrics

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// maxSamples bounds the in-memory ring of recent upload-speed samples.
const maxSamples = 600

// Resources is the process resource snapshot rendered by the dashboard's
// CPU / RAM / bandwidth panel.
type Resources struct {
	CPUPct          float64 `json:"cpu_pct"`
	RAMUsedBytes    int64   `json:"ram_used_bytes"`
	RAMTotalBytes   int64   `json:"ram_total_bytes"`
	RAMPct          float64 `json:"ram_pct"`
	BandwidthBPS    int64   `json:"bandwidth_bps"`
	LinkCapacityBPS int64   `json:"link_capacity_bps"`
	BandwidthPct    float64 `json:"bandwidth_pct"`
}

// Snapshot is the metrics-owned slice of dashboard state; the web layer
// composes it with config, state, and metering for the full /api/stats.
type Snapshot struct {
	UptimeSeconds   int64     `json:"uptime_seconds"`
	TrackedUpload   int64     `json:"tracked_upload"`
	TrackedDownload int64     `json:"tracked_download"`
	UploadBPS       int64     `json:"upload_bps"`
	DownloadBPS     int64     `json:"download_bps"`
	DeficitBytes    int64     `json:"deficit_bytes"`
	WorkersActive   int       `json:"workers_active"`
	CurveIntensity  float64   `json:"curve_intensity"`
	FakeBytesCycle  int64     `json:"fake_bytes_cycle"`
	FakeBytesTotal  int64     `json:"fake_bytes_total"`
	Resources       Resources `json:"resources"`
	SpeedSamples    []int64   `json:"speed_samples"`
}

// Registry holds the live, concurrency-safe in-memory metrics.
type Registry struct {
	startTime time.Time

	fakeBytesTotal  atomic.Int64
	fakeBytesCycle  atomic.Int64
	deficit         atomic.Int64
	workersActive   atomic.Int64
	trackedUpload   atomic.Int64
	trackedDownload atomic.Int64

	mu             sync.RWMutex
	upBPS          int64
	downBPS        int64
	lastUp         int64
	lastDown       int64
	lastObs        time.Time
	curveIntensity float64
	resources      Resources
	samples        []int64
}

// New returns a Registry with its uptime clock started.
func New() *Registry {
	return &Registry{startTime: time.Now()}
}

// AddFakeBytes records n bytes of manufactured traffic.
func (r *Registry) AddFakeBytes(n int64) {
	r.fakeBytesTotal.Add(n)
	r.fakeBytesCycle.Add(n)
}

// StartCycle resets the per-cycle fake-byte counter.
func (r *Registry) StartCycle() { r.fakeBytesCycle.Store(0) }

// SetDeficit records the current deficit in bytes.
func (r *Registry) SetDeficit(d int64) { r.deficit.Store(d) }

// SetWorkers records the number of active workers.
func (r *Registry) SetWorkers(n int) { r.workersActive.Store(int64(n)) }

// SetCurveIntensity records the latest traffic-curve intensity.
func (r *Registry) SetCurveIntensity(v float64) {
	r.mu.Lock()
	r.curveIntensity = v
	r.mu.Unlock()
}

// SetResources records the latest process resource sample.
func (r *Registry) SetResources(res Resources) {
	r.mu.Lock()
	r.resources = res
	r.mu.Unlock()
}

// ObserveCounters records the tracked lifetime counters at time now and
// derives the instantaneous up/down speed from the delta vs the previous call.
// A backward step (a counter reset) clamps the derived speed to zero.
func (r *Registry) ObserveCounters(now time.Time, up, down int64) {
	r.trackedUpload.Store(up)
	r.trackedDownload.Store(down)
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.lastObs.IsZero() {
		if dt := now.Sub(r.lastObs).Seconds(); dt > 0 {
			r.upBPS = int64(float64(up-r.lastUp) / dt)
			r.downBPS = int64(float64(down-r.lastDown) / dt)
			if r.upBPS < 0 {
				r.upBPS = 0
			}
			if r.downBPS < 0 {
				r.downBPS = 0
			}
		}
	}
	r.lastUp, r.lastDown, r.lastObs = up, down, now
	r.samples = append(r.samples, r.upBPS)
	if len(r.samples) > maxSamples {
		r.samples = append([]int64(nil), r.samples[len(r.samples)-maxSamples:]...)
	}
}

// Snapshot returns a consistent copy of the current metrics.
func (r *Registry) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return Snapshot{
		UptimeSeconds:   int64(time.Since(r.startTime).Seconds()),
		TrackedUpload:   r.trackedUpload.Load(),
		TrackedDownload: r.trackedDownload.Load(),
		UploadBPS:       r.upBPS,
		DownloadBPS:     r.downBPS,
		DeficitBytes:    r.deficit.Load(),
		WorkersActive:   int(r.workersActive.Load()),
		CurveIntensity:  r.curveIntensity,
		FakeBytesCycle:  r.fakeBytesCycle.Load(),
		FakeBytesTotal:  r.fakeBytesTotal.Load(),
		Resources:       r.resources,
		SpeedSamples:    append([]int64(nil), r.samples...),
	}
}

// Prometheus renders the metrics as a Prometheus text exposition.
func (r *Registry) Prometheus() string {
	s := r.Snapshot()
	var b strings.Builder
	fmt.Fprintf(&b, "tavazon_upload_bytes_total %d\n", s.TrackedUpload)
	fmt.Fprintf(&b, "tavazon_download_bytes_total %d\n", s.TrackedDownload)
	fmt.Fprintf(&b, "tavazon_fake_bytes_total %d\n", s.FakeBytesTotal)
	fmt.Fprintf(&b, "tavazon_deficit_bytes %d\n", s.DeficitBytes)
	fmt.Fprintf(&b, "tavazon_workers_active %d\n", s.WorkersActive)
	fmt.Fprintf(&b, "tavazon_upload_bps %d\n", s.UploadBPS)
	fmt.Fprintf(&b, "tavazon_curve_intensity %g\n", s.CurveIntensity)
	fmt.Fprintf(&b, "tavazon_cpu_percent %g\n", s.Resources.CPUPct)
	fmt.Fprintf(&b, "tavazon_ram_used_bytes %d\n", s.Resources.RAMUsedBytes)
	fmt.Fprintf(&b, "tavazon_bandwidth_bps %d\n", s.Resources.BandwidthBPS)
	fmt.Fprintf(&b, "tavazon_uptime_seconds %d\n", s.UptimeSeconds)
	return b.String()
}
