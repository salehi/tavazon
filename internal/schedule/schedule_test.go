package schedule

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/salehi/tavazon/internal/config"
)

func defaultAnchors() [24]float64 { return config.Default().Curve.Anchors }

func TestCurveBasePassesThroughAnchors(t *testing.T) {
	a := defaultAnchors()
	for h := 0; h < 24; h++ {
		got := curveBase(a, float64(h))
		if math.Abs(got-a[h]) > 1e-9 {
			t.Errorf("curveBase(%d) = %v, want anchor %v", h, got, a[h])
		}
	}
}

// TestCurveBaseIsContinuous proves the curve never steps: a per-hour step
// function would jump by a whole anchor gap (>0.1) at integer hours, while the
// spline's change between dense samples stays tiny.
func TestCurveBaseIsContinuous(t *testing.T) {
	a := defaultAnchors()
	const step = 0.001
	maxJump := 0.0
	prev := curveBase(a, 0)
	for h := step; h < 48; h += step {
		v := curveBase(a, h)
		if d := math.Abs(v - prev); d > maxJump {
			maxJump = d
		}
		prev = v
	}
	if maxJump > 0.01 {
		t.Errorf("curve is not continuous: max consecutive jump %v", maxJump)
	}
}

func TestCurveBaseIsPeriodic(t *testing.T) {
	a := defaultAnchors()
	for h := 0.0; h < 24; h += 0.37 {
		if d := math.Abs(curveBase(a, h) - curveBase(a, h+24)); d > 1e-6 {
			t.Errorf("curveBase not periodic at h=%v: diff %v", h, d)
		}
	}
}

func TestCurveReachesTrough(t *testing.T) {
	// anchors[4] and [5] are the 0.05 pre-dawn trough.
	if v := curveBase(defaultAnchors(), 4.5); v > 0.2 {
		t.Errorf("pre-dawn curve = %v, expected near zero", v)
	}
}

func TestIntensityStaysInBounds(t *testing.T) {
	cfg := config.Default().Curve
	c := NewCurve(cfg, rand.New(rand.NewPCG(1, 2)))
	start := time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5000; i++ {
		v := c.Intensity(start.Add(time.Duration(i) * 20 * time.Second))
		if v < 0 || v > cfg.Max {
			t.Fatalf("intensity %v out of [0,%v] at step %d", v, cfg.Max, i)
		}
	}
}

func TestRatioDeficit(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	cfg := config.Default().Target.Ratio // multiplier 8, jitter 0.3, min 1 GiB
	const giB = int64(1) << 30

	if d := RatioDeficit(0, giB, cfg, rng); d <= 0 {
		t.Errorf("under-uploaded: deficit = %d, want positive", d)
	}
	if d := RatioDeficit(100*giB, giB, cfg, rng); d != 0 {
		t.Errorf("over-uploaded: deficit = %d, want 0", d)
	}
	// up = 11 GiB, down = 1 GiB: even the +jitter target (~10.4 GiB) leaves a
	// negative deficit, so it is always below min_deficit.
	if d := RatioDeficit(11*giB, giB, cfg, rng); d != 0 {
		t.Errorf("balanced: deficit = %d, want 0", d)
	}
}

func TestRatioDeficitJitterBounds(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 9))
	cfg := config.RatioConfig{Multiplier: 8, Jitter: 0.3, MinDeficitBytes: 0}
	down := int64(1) << 30
	lo := int64(float64(down) * 8 * 0.7)
	hi := int64(float64(down) * 8 * 1.3)
	for i := 0; i < 1000; i++ {
		d := RatioDeficit(0, down, cfg, rng)
		if d < lo-2 || d > hi+2 {
			t.Fatalf("deficit %d outside jittered range [%d,%d]", d, lo, hi)
		}
	}
}

func TestVolumeDeficit(t *testing.T) {
	c := NewCurve(config.Default().Curve, rand.New(rand.NewPCG(1, 2)))
	cfg := config.VolumeConfig{Bytes: 1 << 30, Window: config.Duration(24 * time.Hour)}
	start := time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)

	if d := VolumeDeficit(start, start, 0, cfg, c); d != 0 {
		t.Errorf("at window start: deficit = %d, want 0", d)
	}
	end := start.Add(24 * time.Hour)
	if d := VolumeDeficit(end, start, 0, cfg, c); d != cfg.Bytes {
		t.Errorf("at window end: deficit = %d, want full budget %d", d, cfg.Bytes)
	}
	if d := VolumeDeficit(start.Add(time.Hour), start, cfg.Bytes, cfg, c); d != 0 {
		t.Errorf("over-sent: deficit = %d, want 0", d)
	}
	// Mid-window the curve-weighted schedule is between 0 and the full budget.
	mid := VolumeDeficit(start.Add(12*time.Hour), start, 0, cfg, c)
	if mid <= 0 || mid >= cfg.Bytes {
		t.Errorf("mid-window: deficit = %d, want strictly inside (0,%d)", mid, cfg.Bytes)
	}
}
