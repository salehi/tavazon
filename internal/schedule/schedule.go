// Package schedule computes how much traffic to manufacture (the ratio and
// volume target modes) and the continuous 24-hour traffic curve.
// See docs/project.md §6.3, §6.4, §7.6.
package schedule

import (
	"math"
	"math/rand/v2"
	"time"

	"tavazon/internal/config"
)

// RatioDeficit returns how many bytes of fake upload are needed to keep
// upload = download x multiplier. The multiplier is jittered by +/-Jitter each
// call. A deficit below MinDeficitBytes (including a negative one) returns 0.
func RatioDeficit(trackedUp, trackedDown int64, cfg config.RatioConfig, rng *rand.Rand) int64 {
	jitter := 1 + (rng.Float64()*2-1)*cfg.Jitter
	target := int64(float64(trackedDown) * cfg.Multiplier * jitter)
	deficit := target - trackedUp
	if deficit < cfg.MinDeficitBytes {
		return 0
	}
	return deficit
}

// VolumeDeficit returns how many bytes are owed right now under volume mode:
// the curve-weighted share of the budget that should have been sent between
// windowStart and now, minus windowSent. It never returns a negative value.
// Window roll-over is the engine's responsibility; this function is pure.
func VolumeDeficit(now, windowStart time.Time, windowSent int64, cfg config.VolumeConfig, c *Curve) int64 {
	window := cfg.Window.Std()
	if window <= 0 {
		return 0
	}
	total := curveIntegral(c, windowStart, windowStart.Add(window))
	var frac float64
	if total > 0 {
		frac = curveIntegral(c, windowStart, now) / total
	} else {
		// Degenerate flat-zero curve: fall back to a linear schedule.
		elapsed := now.Sub(windowStart)
		if elapsed < 0 {
			elapsed = 0
		}
		if elapsed > window {
			elapsed = window
		}
		frac = float64(elapsed) / float64(window)
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	deficit := int64(float64(cfg.Bytes)*frac) - windowSent
	if deficit < 0 {
		return 0
	}
	return deficit
}

// curveIntegral approximates the integral of the (wander-free) base curve over
// [from, to], in intensity-hours, by the trapezoid rule at one-minute steps.
// Samples are clamped to >= 0 so the integral is monotonic non-decreasing.
func curveIntegral(c *Curve, from, to time.Time) float64 {
	if !to.After(from) {
		return 0
	}
	const step = time.Minute
	var sum float64
	for t := from; t.Before(to); t = t.Add(step) {
		end := t.Add(step)
		if end.After(to) {
			end = to
		}
		a := math.Max(0, curveBase(c.anchors, tehranHour(t)))
		b := math.Max(0, curveBase(c.anchors, tehranHour(end)))
		sum += (a + b) / 2 * end.Sub(t).Hours()
	}
	return sum
}
