# Tavazon — Engineering Standards

> How Tavazon code is written, structured, tested, and reviewed. Where this document
> states a *constraint* (must / never), it is binding; where it states a *convention*,
> follow it unless there is a documented reason not to.
>
> Hard project boundaries live in [RED_LINES.md](RED_LINES.md). This document covers
> everyday craft. The full design is [project.md](project.md).

---

## 1. Language & tooling

- **Go 1.22+.** `go.mod` declares `go 1.22`. Use language features available there
  (`math/rand/v2`, generics where they genuinely simplify).
- **One vendored dependency.** `maxminddb-golang` only; everything else is stdlib
  (RED_LINES X1). No new third-party import without a project.md change.
- **`gofmt` is law.** All code is `gofmt`-formatted.
- **`go vet ./...` is clean.** No exceptions, no suppressions. Run it in the toolchain
  container (project.md §16) before every push.
- **Build with `CGO_ENABLED=0` and `-mod=vendor`** everywhere (RED_LINES X1, X2) — the
  build must succeed fully offline.
- **No Go on the host, no `Makefile`.** Every toolchain command runs inside the
  `golang` container described in project.md §16.

---

## 2. Package & file layout

- Layout follows [project.md §5](project.md) exactly. New packages go under
  `internal/`.
- One package = one responsibility. Pure-logic packages (`config`, `netstat`,
  `schedule`, `targets`, `metering`'s percentile math, parts of `state` and
  `uploader`) do **no I/O** beyond what their name implies, so they unit-test without
  sockets, `/proc`, or real GeoLite2 files.
- `cmd/tavazon/main.go` is wiring only (RED_LINES X3).
- `vendor/` is committed and never hand-edited — regenerate it with the `vendor`
  command in the toolchain container (project.md §16).
- Test files sit next to their package as `*_test.go`. Fixtures go in `testdata/`
  (e.g. the `/proc/net/dev` sample). No `.mmdb` fixture is committed — `geoip` tests
  use the operator's real files or `geoip.NewForTest` (RED_LINES X12).

---

## 3. Naming

- Exported identifiers have doc comments starting with the identifier name.
- Names describe role, not type: `trackedUpload`, not `tu`; `effMul`, not `m2`.
- Acronyms keep case: `ASN`, `IPCache`, `RxBytes`, `TxBytes`, `UDP`, `TTL`.
- Config struct fields are Go-cased; their JSON tags are `snake_case` matching
  [project.md §8](project.md) verbatim.

---

## 4. Error handling

This is the single biggest fix over the original (project.md §2, flaw #3).

- **Every error is checked.** No bare `_ = err` (RED_LINES X5).
- Wrap with context when propagating: `fmt.Errorf("open asn db %q: %w", path, err)`.
- Errors that should not be fatal — a transient network or file error inside a worker
  or a cycle — are **logged and survived**, never propagated up to kill the daemon.
- Errors that *are* fatal happen only at startup (bad config, unparseable durations,
  missing `.mmdb` files): log, exit non-zero, before the engine loop begins. Missing
  GeoIP data keeps the dashboard up so the operator can see the problem (project.md §6.5).
- `Validate()` catches bad config *before* the loop starts, not mid-run.

---

## 5. Panic recovery

- Every long-lived goroutine — engine loop, web server, metering writer, state-save
  ticker, every upload worker — wraps its body in
  `defer func(){ if r := recover(); r != nil {...} }()` that logs the panic value
  **and a stack trace** with context (RED_LINES X4).
- The **engine loop** recovers per-iteration and continues; a panic in cycle N must
  not stop cycle N+1.
- A worker panic ends that worker only; the cycle's `WaitGroup` still completes.
- `recover()` is for resilience, never for normal control flow.

---

## 6. Concurrency

- Follow the model in [project.md §13](project.md): main goroutine = engine, optional
  web goroutine, per-cycle workers joined by `sync.WaitGroup`, one metering writer, one
  state-save ticker.
- No worker outlives its cycle. Worker count is bounded by `max_workers` (RED_LINES E3).
- Shared state is explicitly guarded — `Config` behind `RWMutex`, `State` behind
  `Mutex`, metrics via `atomic` plus an `RWMutex`. `geoip.GeoIP` is read-only after
  `Open`, so it needs no lock.
- The metering writer drains a buffered channel; the engine never blocks on disk I/O.
- No module-global mutable state shared across goroutines (RED_LINES X9).
- A single `context.Context` from `signal.NotifyContext` threads through everything;
  every blocking loop checks `ctx.Done()`. Cancellation = graceful shutdown.
- Run the race detector via the toolchain container: `go test -race -mod=vendor ./...`
  (project.md §16).

---

## 7. Logging

- `log/slog`, JSON records, one per line, via the size-rotating writer
  (project.md §7.12, §11.2). Mirror to stderr at the configured level.
- Log the events listed in project.md §11.2 — and no high-frequency spam: per-packet
  or per-worker lines belong at `debug`, never `info`.
- Every recovered panic and every non-fatal error is logged **with context**
  (which IP, which ASN, which file, which cycle).
- Byte sizes in messages are human-formatted (`12.34 GiB`) via the `humanize` helper.
- Never open/close the log file per line (project.md §2, flaw #9).

---

## 8. Configuration

- Defaults, JSON tags, and value ranges match [project.md §8](project.md) exactly.
- `Validate()` range-checks every coefficient, rejects unparseable durations, and
  confirms both `.mmdb` paths exist (project.md §7.2). Out-of-range input is rejected,
  never silently clamped — except where project.md explicitly says to clamp.
- Precedence is fixed: defaults → `config.json` → `TAVAZON_*` env → CLI flags
  (project.md §12). Tests assert this ordering.
- Config is reloadable at runtime behind an `RWMutex`; a `PUT /api/config` takes
  effect next cycle without a restart, and **every accepted change is written to the
  metering audit log** (project.md §6.10).
- `Validate()` warns loudly when `web.listen` is non-local and `auth_token` is empty.

---

## 9. The traffic curve & DPI resistance

These two properties are explicit project requirements — treat them as standards, not
nice-to-haves.

- The 24-hour curve is a **continuous, C1, periodic** function (project.md §6.4). It
  must have **no step on the hour** and **no seam** at the 23→0 wrap. Worker-count
  changes between cycles are slew-limited so the realised traffic ramps, never jumps.
- Payloads are **fully random** in size, content, destination port, and timing
  (project.md §6.9). No constant may exist that a DPI signature could match: no fixed
  size buckets, no static header, no fixed port list, no protocol mimicry.
- Any change that reintroduces a periodic, modal, or constant pattern into the traffic
  is a regression even if it compiles and tests pass.

---

## 10. Testing

- **Every pure-logic package has unit tests** covering the cases in
  [project.md §15](project.md). No real sockets, no real `/proc`, no real GeoLite2 —
  use fixtures and injected seams (`parse(r io.Reader, ...)`, `geoip.NewForTest`, a
  `netstatFn` in the engine). The sole exception is `geoip`'s own test, which reads
  the operator's real `.mmdb` files and skips when they are absent.
- Table-driven tests are the default style.
- Property-style assertions for the algorithmic packages: the curve is C1-continuous
  and periodic and can reach ~0 at the trough; generated IPs always fall inside their
  ASN's prefixes; `RandomSize` never exceeds `max_datagram`; the token bucket delivers
  ≈ the configured rate over a window; the 95th-percentile calc matches a known sample
  set.
- One integration test: local UDP listener on `127.0.0.1`, a one-ASN
  `geoip.NewForTest` graph, a single short `RunCycle`; assert bytes received, `fake_bytes_total`
  advanced, and metering recorded the ASN.
- `go test -race ./...` (in the toolchain container) is green before every push.
- A bug fix lands with a test that fails before it and passes after.

---

## 11. Comments & docs

- Comment *why*, not *what*. The code says what.
- Every exported symbol has a doc comment.
- Where an algorithm implements a project.md section, cite it: `// see project.md §6.8`.
- Keep [project.md](project.md) the source of truth: if code must diverge from it,
  update project.md in the same change.

---

## 12. The flaws acceptance checklist

The rewrite exists to fix the structural flaws in the original (project.md §2). Each
flaw below must be demonstrably addressed; a reviewer signs this off before the
project is considered complete.

| # | Original flaw | Resolved by | Verified by |
|---|---------------|-------------|-------------|
| 1 | Triple `menu` import name collision | No TUI; web dashboard replaces it | code review |
| 2 | Invalid-speed input calls wrong setter | Config validation + typed handlers | `config_test.go` |
| 3 | No exception handling anywhere | §4 error handling + §5 panic recovery | review + tests |
| 4 | Recursion for control flow | Iteration only (RED_LINES X11) | review |
| 5 | All-zero / fixed-bucket UDP payload | Fully-random payload (project.md §6.9) | `uploader_test.go` |
| 6 | Counters sum loopback/VPN | `netstat` excludes `lo`, selects ifaces | `netstat_test.go` |
| 7 | Reboot needs *both* counters to drop | Triggers on *either* (project.md §6.2) | engine test |
| 8 | One OS thread per IP | Bounded worker pool + token bucket | `uploader_test.go` |
| 9 | Log opened per line, never rotates | Persistent rotating writer (§7.12) | review + test |
| 10 | No headless config | JSON + env + flags (project.md §8, §12) | `config_test.go` |
| 11 | Hardcoded absolute paths | All paths from config/flags (RED_LINES X7) | review |
| 12 | Module-global mutable shared state | Explicit synchronization (RED_LINES X9) | review + `-race` |
| 13 | No tests, no type/quality discipline | §10 testing + containerised gates (no CI, no Makefile by design) | local gates green |
| 14 | Hard Redis dependency | Local atomic JSON state (RED_LINES X6) | review |
| 15 | Tri-state Redis-polled TUI flag | No TUI; Start/Stop is a persisted `general.running` flag read live | review |
| 16 | Static, hand-maintained IP list | Live MaxMind ASN targeting (project.md §6.5) | `geoip`/`targets` tests |
| 17 | Stepwise hourly traffic curve | Continuous C1 periodic curve (project.md §6.4) | `schedule_test.go` |
| 18 | No history / metering | `metering` store + dashboard history (§6.10) | `metering_test.go` |

---

## 13. Change review checklist

There is no CI (project.md §16); reviewers apply this list by hand. Before approving
any change:

- [ ] In the toolchain container (project.md §16): `gofmt -l .` empty; `go vet` clean;
      `go test -race` green; `go build` succeeds offline (`-mod=vendor`).
- [ ] No new third-party import; the allowlist is unchanged (RED_LINES X1).
- [ ] No `.mmdb` committed at all — not even a test fixture (RED_LINES X12).
- [ ] Crosses no line in [RED_LINES.md](RED_LINES.md).
- [ ] Every error checked; every new goroutine has panic recovery.
- [ ] No new periodic/modal/constant pattern in generated traffic (§9).
- [ ] New behavior has tests; bug fixes have a regression test.
- [ ] Exported symbols documented; project.md updated if the design moved.
- [ ] Touches the flaws table only to *resolve*, never to reintroduce.
