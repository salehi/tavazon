package schedule

import (
	"math"
	"math/rand/v2"
	"time"

	"namizungo/internal/config"
)

var tehran = loadTehran()

func loadTehran() *time.Location {
	loc, err := time.LoadLocation("Asia/Tehran")
	if err != nil {
		return time.UTC
	}
	return loc
}

// Curve is the continuous 24-hour traffic-intensity curve (docs/project.md
// §6.4): a periodic Catmull-Rom spline through 24 hourly anchors, modulated by
// a slow mean-reverting random wander. It is stateful and NOT safe for
// concurrent use; the engine drives it from a single goroutine.
type Curve struct {
	anchors         [24]float64
	max             float64
	wanderStrength  float64
	wanderReversion time.Duration
	wander          float64
	lastUpdate      time.Time
	rng             *rand.Rand
}

// NewCurve builds a Curve from config. rng drives the wander process.
func NewCurve(cfg config.CurveConfig, rng *rand.Rand) *Curve {
	return &Curve{
		anchors:         cfg.Anchors,
		max:             cfg.Max,
		wanderStrength:  cfg.WanderStrength,
		wanderReversion: cfg.WanderReversion.Std(),
		rng:             rng,
	}
}

// Intensity returns the traffic-intensity multiplier for the given instant:
// the spline value at the Tehran-local time of day, scaled by the wander and
// clamped to [0, max].
func (c *Curve) Intensity(now time.Time) float64 {
	c.advanceWander(now)
	v := curveBase(c.anchors, tehranHour(now)) * (1 + c.wander)
	if v < 0 {
		return 0
	}
	if v > c.max {
		return c.max
	}
	return v
}

// advanceWander steps the Ornstein-Uhlenbeck wander forward to now. It is the
// exact OU update toward mean 0 with stationary standard deviation
// wanderStrength and time constant wanderReversion.
func (c *Curve) advanceWander(now time.Time) {
	if c.lastUpdate.IsZero() {
		c.lastUpdate = now
		return
	}
	dt := now.Sub(c.lastUpdate).Seconds()
	c.lastUpdate = now
	tau := c.wanderReversion.Seconds()
	if dt <= 0 || tau <= 0 || c.rng == nil {
		return
	}
	decay := math.Exp(-dt / tau)
	c.wander = c.wander*decay + c.wanderStrength*math.Sqrt(1-decay*decay)*c.rng.NormFloat64()
}

// tehranHour returns the fractional hour-of-day (0..24) in Asia/Tehran.
func tehranHour(t time.Time) float64 {
	lt := t.In(tehran)
	return float64(lt.Hour()) +
		float64(lt.Minute())/60 +
		float64(lt.Second())/3600 +
		float64(lt.Nanosecond())/3.6e12
}

// curveBase evaluates the periodic Catmull-Rom spline through the 24 hourly
// anchors at fractional hour h. It is C1-continuous and seamless across the
// 23->0 wrap, so it never steps.
func curveBase(anchors [24]float64, h float64) float64 {
	h = math.Mod(h, 24)
	if h < 0 {
		h += 24
	}
	i := int(math.Floor(h))
	t := h - float64(i)
	p0 := anchors[(i+23)%24]
	p1 := anchors[i%24]
	p2 := anchors[(i+1)%24]
	p3 := anchors[(i+2)%24]
	return catmullRom(p0, p1, p2, p3, t)
}

// catmullRom evaluates the uniform Catmull-Rom spline segment between p1 and
// p2 (with neighbours p0, p3) at parameter t in [0,1].
func catmullRom(p0, p1, p2, p3, t float64) float64 {
	t2 := t * t
	t3 := t2 * t
	return 0.5 * (2*p1 +
		(-p0+p2)*t +
		(2*p0-5*p1+4*p2-p3)*t2 +
		(-p0+3*p1-3*p2+p3)*t3)
}
