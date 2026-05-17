# Phase 3 — Traffic engine & observability

> Plan for [ROADMAP.md](../ROADMAP.md) Phase 3. The part that moves bytes, plus the
> metering, metrics, and logging that record it.

**Delivers:** `internal/uploader`, `internal/metering`, `internal/metrics`,
`internal/sysstat`, `internal/logging`.
**Depends on:** Phase 1–2 (`config`, `state`, `targets`).
**Unblocks:** Phase 4 (engine).

Build order: `logging` → `metrics` → `sysstat` → `metering` → `uploader` (uploader
records into metrics + metering, so those come first).

---

## 3.1 `internal/logging`

Implements [project.md §7.12, §11.2](../project.md). Fixes flaw #9.

- `rotatingWriter` — an `io.Writer` that tracks bytes written; when the file exceeds
  `MaxSizeMB` it renames `file → file.1 → file.2 …` up to `MaxBackups`, then reopens.
  The file handle stays **open** between writes (never per-line open/close).
- `Setup(cfg config.LogConfig) (*slog.Logger, func() error, error)` — a
  `slog.JSONHandler` writing to an `io.MultiWriter(rotatingWriter, os.Stderr)` at the
  configured level; the returned `func` flushes/closes on shutdown.
- `humanize(n int64) string` — `"12.34 GiB"` style, used in log messages.

**`logging_test.go`:** writing past `MaxSizeMB` produces `file.1`; `MaxBackups`
generations are kept and the oldest dropped; `humanize` table.

## 3.2 `internal/metrics`

Implements [project.md §7.11, §11.1](../project.md). In-memory, concurrency-safe.

- `Registry` — `atomic.Int64` counters (`fakeBytesTotal`, `fakeBytesCycle`,
  `deficit`, `workersActive`, `upBytes`, `downBytes`) + an `RWMutex`-guarded ring
  buffer of recent per-second speed samples + the latest resource sample (CPU%, RSS,
  bandwidth %) + a `startTime`.
- `AddFakeBytes(n int64)`, `ObserveCounters(up, down int64)` (computes up/down bps
  from the delta vs the previous call), `SetDeficit(int64)`, `SetWorkers(int)`,
  `SetResources(cpuPct, ramPct, bwPct float64, rss, memTotal, linkBPS int64)`,
  `StartCycle()` (resets `fakeBytesCycle`).
- `Snapshot() Snapshot` — the struct behind `/api/stats` (project.md §10.2).
- `Prometheus() string` — hand-built text exposition (project.md §11.1).

**`metrics_test.go`:** counters are race-clean under concurrent `AddFakeBytes`;
`ObserveCounters` computes correct bps from a known delta + interval; `Prometheus`
output parses.

## 3.2a `internal/sysstat`

Implements [project.md §7.11a](../project.md). Process resource sampler — stdlib only,
Linux-only (like `netstat`).

- `Sample{CPUJiffies uint64; RSSBytes, MemTotalBytes int64; At time.Time}`.
- `Read() (Sample, error)` — parse `/proc/self/stat` (`utime+stime`),
  `/proc/self/status` (`VmRSS`), `/proc/meminfo` (`MemTotal`). A `parse` helper per
  file is split out for fixture tests.
- `CPUPercent(prev, cur Sample) float64` — `Δjiffies / clkTck / Δwall / NumCPU × 100`;
  `clkTck` is the standard Linux `100` (documented constant — no cgo `sysconf`).
- The engine samples this once per cycle and feeds `metrics.SetResources` (Phase 4);
  the bandwidth gauge uses `network.link_capacity_mbit` (0 ⇒ read
  `/sys/class/net/<iface>/speed`).

**`sysstat_test.go`:** `parse` against checked-in `/proc/self/stat`, `status`, and
`meminfo` fixtures; `CPUPercent` against two hand-built samples with a known delta.

## 3.3 `internal/metering`

Implements [project.md §6.10, §9.2](../project.md). The long-horizon store — flaw #18.

**Files & responsibilities:**

- `metering.go` — the store: `Open`, `Sample`, `RecordSend`, `History`, rollups.
- `percentile.go` — the 95th-percentile billing calc.
- `audit.go` — the append-only config-change audit log.

**Types & functions:**

- `Store` — owns the `data/metering/` directory; a buffered `chan sample` drained by
  one writer goroutine so the engine never blocks on disk (project.md §13).
- `Open(cfg config.MeteringConfig) (*Store, error)` — create the dir; restore the
  per-second ring from `live.bin` if present; open today's `*.5min.jsonl` for append.
- `(*Store) Sample(now time.Time, real netstat.Counters, fake int64)` — feed the
  per-second ring; every 5 min flush a bucket line (project.md §9.2 shape) to the
  monthly file; at day roll-over append a `daily.jsonl` rollup; enforce
  `retention_5min` by deleting old monthly files.
- `(*Store) RecordSend(asn uint32, n int64)` — accumulate fake bytes per ASN into the
  current 5-minute bucket.
- `(*Store) History(from, to time.Time, gran string) ([]Bucket, error)` — read back
  from the live ring / 5-min files / daily file depending on `gran`.
- `(*Store) Percentile() Billing` — `percentile.go`: collect the 5-minute samples in
  the billing window, sort, drop the top `(100−percentile)%`, return the next sample
  as the charged rate, for upload and download, plus total volume.
- `(*Store) AppendAudit(rec AuditRecord)` — append a config-change line to
  `audit.jsonl`; `Audit(n int) []AuditRecord` reads the last `n` back.
- `(*Store) Close()` — drain the channel, snapshot the ring to `live.bin`.

**Sampling note:** the 95th-percentile and volume figures are computed from the
**real interface counters**, not just Tavazon's fake bytes — the provider bills the
whole NIC. Per-ASN breakdown covers only the fake portion (the only attributable
traffic). State this in code comments.

**`metering_test.go`:** 5-minute bucketing boundaries; `Percentile` against a known
sample set (hand-computed expected value); per-ASN totals sum correctly; daily
rollup equals the sum of its buckets; `audit.jsonl` round-trips; old monthly files
past retention are pruned.

## 3.4 `internal/uploader`

Implements [project.md §6.8, §6.9](../project.md). Fixes flaws #5 and #8.

**`payload.go`:**

- `RandomSize(cfg config.UploaderConfig, rng) int` — **uniform** in
  `[MinDatagram, MaxDatagram]`; no buckets, no weighting. Never exceeds `MaxDatagram`.
- `Random(buf []byte, rng)` — fill with fresh `math/rand/v2` bytes (reuse a buffer).
- `tokenBucket` — `refill = baseRate · SpeedCoefficient` bytes/sec; `WaitN(ctx, n)`
  blocks until `n` tokens are available or `ctx` is done.

**`uploader.go`:**

- `Uploader{targets *targets.Targets; state *state.State; metrics *metrics.Registry;
  metering *metering.Store; cfg func() config.UploaderConfig;
  dev func() config.DevConfig; limiter *tokenBucket}` — `cfg` and `dev` are closures
  so live config reloads (including the dev-mode toggle) are picked up.
- `RunCycle(ctx context.Context, budget int64, workers int) int64` — spawn exactly
  `workers` goroutines (a `WaitGroup`); each picks a target via `pickTarget()`: when
  `dev().Enabled` it returns `dev().Target` with a random port and `asn = 0`
  (project.md §6.6); otherwise the TTL cache or `targets.RandomTarget()`. Then
  `DialUDP`, loop sending random datagrams under the token bucket with a random
  inter-packet sleep until its quota is met or `ctx` is cancelled; errors are logged
  and the loop continues; record bytes into metrics + metering per ASN. Every worker
  body has `defer recover()`.

**`uploader_test.go`** (no real internet — listen on `127.0.0.1`):

- `RandomSize` is uniform-ish and never exceeds `MaxDatagram`; `Random` output is
  non-constant.
- `tokenBucket` delivers ≈ the configured rate over a measured window.
- `RunCycle` against a local UDP sink: spawns exactly `min(workers, MaxWorkers)`
  goroutines, is cancellable mid-cycle via `ctx`, returns bytes ≈ budget, records
  per-ASN metering.
- with the `dev` closure returning `Enabled: true`, every datagram is sent to
  `dev().Target` regardless of the ASN pools (project.md §6.6).

---

## Definition of done

- [ ] `go test -race ./internal/{logging,metrics,metering,uploader}` green.
- [ ] Datagrams uniform in `[min,max]`, never fragmented, never constant (flaw #5).
- [ ] Worker pool bounded by `MaxWorkers`, `ctx`-cancellable (flaw #8).
- [ ] Token bucket holds the configured rate; log rotates at `MaxSizeMB` (flaw #9).
- [ ] `Percentile` matches a hand-computed expected value; per-ASN totals correct.
- [ ] Flaws #5, #8, #9, #18 (STANDARDS.md §12) addressed.
- [ ] `gofmt`/`vet` clean; build offline.
