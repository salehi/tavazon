# Phase 4 — Orchestration

> Plan for [ROADMAP.md](../ROADMAP.md) Phase 4. Wire Phases 1–3 into the cycle loop.

**Delivers:** `internal/engine`.
**Depends on:** Phase 1–3 (every other `internal/` package).
**Unblocks:** Phase 5 (web + main wire the engine in).

---

## 4.1 `internal/engine`

Implements the loop of [project.md §6.11](../project.md).

**Type:**

```go
type Engine struct {
    cfg      func() *config.Config   // closure → always the current (reloadable) config
    state    *state.State
    netstat  func(iface string) (netstat.Counters, error) // injectable for tests
    sysstat  func() (sysstat.Sample, error)               // injectable for tests
    prevSys  sysstat.Sample
    sched    *schedule.Curve
    uploader *uploader.Uploader
    metering *metering.Store
    metrics  *metrics.Registry
    log      *slog.Logger
    prevWorkers int
}
```

`netstat` is a **function field**, not a direct call, so tests inject a fake counter
source instead of touching `/proc`.

**Functions:**

- `New(...) *Engine` — assemble from the wired dependencies.
- `(*Engine) Run(ctx context.Context) error` — the loop:
  1. on entry log `service started`, `state.PurgeExpired`.
  2. loop until `ctx.Done()`:
     - if `!cfg().General.Running` (Stopped via the dashboard) → short sleep, `continue`.
     - `raw := netstat(iface)`.
     - reboot detection + synchronizer re-anchor (project.md §6.2) — triggers on
       **either** counter dropping (flaw #7).
     - `deficit := schedule.Deficit(mode, …)` (project.md §6.3).
     - `metrics.SetDeficit`; `metering.Sample(now, raw, fakeThisCycle)`.
     - sample `sysstat`, compute CPU% vs `prevSys` and bandwidth %, then
       `metrics.SetResources(...)` for the dashboard resource panel (project.md §7.11a).
     - if `deficit > 0`, uploader enabled, and (`!targets.Empty()` or `cfg().Dev.Enabled`):
       - workers: in **dev mode** (`cfg().Dev.Enabled`) `workers = cfg().Dev.Workers`,
         no curve; otherwise `intensity := sched.Intensity(now)`,
         `target := int(intensity·base)`,
         `workers := min(slew(prevWorkers, target, MaxRamp), MaxWorkers)` — the **slew
         limiter** keeps cycle-to-cycle change smooth so traffic never steps (§6.4).
       - `sent := uploader.RunCycle(ctx, budget, workers)`; `prevWorkers = workers`.
       - log `cycle complete` (bytes, duration, workers, per-ASN).
       - re-read `raw`, recompute tracked totals.
     - `state.Save()` (atomic).
     - sleep `randomDuration(IntervalMin, IntervalMax)` — there is **no hourly tick**;
       time-of-day shaping is entirely the continuous curve's job.
  3. wrap **each iteration body** in `defer recover()` — a panic logs a stack trace
     and the loop continues (flaw #3); the daemon never dies from one bad cycle.
  4. on `ctx` cancel: log `service stopping`, final `state.Save`, return nil.
- **No** `Pause`/`Resume` method. The loop reads `cfg().General.Running` live every
  cycle; the Start/Stop and Dev-mode dashboard buttons are handled entirely by the web
  layer (Phase 5), which mutates the shared config, persists `config.json`, and
  audits. The engine picks the change up next cycle — and on startup reads the
  persisted `general.running` straight from `config.json`, so an unwanted restart
  resumes in the last run state (project.md §6.11).
- `slew(prev, target, maxRamp int) int` — clamp the step to `±maxRamp`.

**Concurrency:** the engine is the main goroutine (project.md §13). It reads `Config`
through the closure (web writes it under an `RWMutex`); it owns `state` mutation but
`state` guards itself; `metrics`/`metering` are already concurrency-safe.

**`engine_test.go`:**

- inject a fake `netstat` returning a scripted sequence; drive a few cycles with a
  short-`ctx` and assert tracked totals advance.
- **reboot:** feed counters that drop on one side only → synchronizer re-anchors and
  the tracked total is continuous (flaw #7).
- a cycle that panics (inject a uploader stub that panics) → the loop logs and
  continues to the next cycle.
- deficit below `MinDeficitBytes` → no `RunCycle` call that cycle.
- setting `general.running` false → cycles become no-ops next cycle; true → they
  resume; the engine reads the flag live (no restart, no method call).
- with a config whose `general.running` is false at load, the engine starts idle —
  proving restart-state restore.
- dev mode on → the cycle uses `dev.workers` and uploads target `dev.target`.
- worker count between consecutive cycles never changes by more than `MaxRamp`
  (production mode).

---

## Definition of done

- [ ] `go test -race ./internal/engine` green.
- [ ] Reboot detection re-anchors on **either** counter dropping (flaw #7).
- [ ] A panicking cycle does not stop the loop (flaw #3).
- [ ] No hourly tick anywhere; worker count is slew-limited and `MaxWorkers`-capped.
- [ ] Run state restored from `config.json` after a restart; dev mode routes all
      traffic to `dev.target` and sizes the cycle from `dev.workers`.
- [ ] State saved atomically every cycle; metering sampled every cycle.
- [ ] `gofmt`/`vet` clean; build offline.
