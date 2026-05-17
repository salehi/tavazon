# Phase 2 — Scheduling & targeting

> Plan for [ROADMAP.md](../ROADMAP.md) Phase 2. The pure-logic brain: *how much* to
> send, *when*, and *where*.

**Delivers:** `internal/schedule` (target modes + the continuous curve),
`internal/targets` (ASN-based IP/port generation).
**Depends on:** Phase 1 (`config`, `geoip`, `state`).
**Unblocks:** Phase 3 (uploader uses targets) and Phase 4 (engine uses schedule).

Build `schedule` first, then `targets`.

---

## 2.1 `internal/schedule` — `curve.go`

Implements the continuous 24-hour curve, [project.md §6.4](../project.md). This is the
fix for flaw #17 — there must be **no on-the-hour step**.

**Types & functions:**

- `Curve` struct — holds `anchors [24]float64`, `max float64`, the wander parameters,
  the current wander value, a `lastUpdate time.Time`, and an `*rand.Rand`.
- `NewCurve(cfg config.CurveConfig, rng *rand.Rand) *Curve`.
- `curveBase(h float64) float64` — **periodic Catmull-Rom** spline through the 24
  anchors, `h ∈ [0,24)`. Wrap the control points (`anchors[23], anchors[0],
  anchors[1]`) so the segment across midnight is C1-continuous with no seam.
- `(*Curve) Intensity(now time.Time) float64` — advance the Ornstein-Uhlenbeck
  `wander` toward 0 by the elapsed time (mean-reverting, strength + reversion from
  config), then return `clamp(curveBase(tehranHour(now)) * (1+wander), 0, max)`.
- `tehranHour(t time.Time) float64` — fractional hour in `Asia/Tehran`.

**Notes:** the curve is sampled once per engine cycle; the engine slew-limits the
*worker count* derived from it (Phase 4). `Intensity` is stateful (the wander walk) —
that is fine, it lives in one goroutine; document it as not safe for concurrent use.

## 2.2 `internal/schedule` — `schedule.go`

Implements both target modes, [project.md §6.3](../project.md).

**Functions:**

- `RatioDeficit(trackedUp, trackedDown int64, cfg config.RatioConfig, rng) int64` —
  `effMul = Multiplier·(1±Jitter)`; `deficit = trackedDown·effMul − trackedUp`;
  return `0` if below `MinDeficitBytes`.
- `VolumeDeficit(now time.Time, st *state.State, cfg config.VolumeConfig, c *Curve)
  int64` — roll the window if `now − WindowStart ≥ Window`; compute
  `scheduled = Bytes · curveIntegral(WindowStart, now) / curveIntegral(WindowStart,
  WindowStart+Window)`; return `scheduled − WindowSentBytes` (≥ 0).
- `curveIntegral(from, to time.Time) float64` — numeric integral of `curveBase` over
  the interval (fixed-step Simpson/trapezoid is plenty); used to spend the volume
  budget along the organic day-shape.
- `Deficit(...)` — a thin dispatcher on `cfg.Target.Mode` so the engine is
  mode-agnostic.

**`schedule_test.go`:**

- **C1 continuity:** `curveBase` sampled densely has no jump at any integer hour;
  `|curveBase(h+ε) − curveBase(h−ε)|` stays below a small bound for all `h`,
  including the `24⁻ ≡ 0` wrap.
- The curve reaches ~0 near the configured pre-dawn trough.
- `Intensity` wander stays within `±` a bound proportional to `WanderStrength` over a
  long simulated run, and reverts toward the base.
- `RatioDeficit`: positive when under-uploaded, `0` below `MinDeficitBytes`, jitter
  stays within `±Jitter`.
- `VolumeDeficit`: integral over a full window equals `Bytes`; window roll-over
  resets the cursor; never returns negative.

---

## 2.3 `internal/targets`

Implements [project.md §6.6](../project.md). Turns selected ASNs into destinations.

**Types & functions:**

- `asnPool{asn uint32; prefixes []net.IPNet; weight uint64}` — `weight` = total IPs.
- `Targets{pools []asnPool; totalWeight uint64; cfg config.TargetsConfig;
  rng *rand.Rand}`.
- `New(g *geoip.GeoIP, selectedASNs []uint32, cfg config.TargetsConfig)
  (*Targets, error)` — resolve each selected ASN to its prefixes via `g.Prefixes`;
  skip (with a warning) any ASN the db does not know; error only if the result is
  empty *and* ASNs were requested.
- `(*Targets) RandomTarget() (ip net.IP, port int, asn uint32)`:
  1. pick a pool weighted by `weight`,
  2. pick a prefix within it weighted by prefix size,
  3. pick a uniformly random host inside the prefix,
  4. pick a uniform random port in `[PortMin, PortMax]`.
- `(*Targets) Empty() bool` — true when no ASN is selected/resolved; the engine uses
  this to stay idle (project.md §6.6).

The TTL IP cache (project.md §6.7) lives in `state`; the uploader (Phase 3) consults
the cache and falls back to `RandomTarget`. Keep `targets` itself pure — no cache, no
sockets.

**`targets_test.go`** (uses `geoip.NewForTest` — no `.mmdb` file needed):

- weighted selection over many draws covers **every** selected ASN.
- every generated IP falls inside one of that ASN's prefixes.
- every port is within `[PortMin, PortMax]`.
- `New` with an unknown ASN warns and continues; with no ASNs → `Empty()` true.

---

## Definition of done

- [ ] `go test -race ./internal/{schedule,targets}` green.
- [ ] Curve proven C1-continuous and periodic — no step at any hour (flaw #17).
- [ ] Ratio and volume `Deficit` correct, including the volume window roll-over.
- [ ] `RandomTarget` covers every selected ASN and stays inside their prefixes/ports.
- [ ] `gofmt`/`vet` clean; build offline.
