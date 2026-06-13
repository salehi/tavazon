# Tavazon — Roadmap

> The build path, phase by phase. This is [project.md §17](project.md)'s 15-step order
> grouped into seven phases, each with a concrete **definition of done**. A phase is
> not "done" until every box is checked — later phases depend on that guarantee.
>
> Build bottom-up: each layer is fully testable before the next one depends on it.
>
> Granular, file-by-file task plans for each phase live in [plans/](plans/) — one
> file per phase, to be worked top to bottom.

---

## Phase 0 — Foundation

*project.md §17 step 1.*

Stand up the repository skeleton so every later phase has somewhere to land.

- `go.mod` — `module tavazon`, `go 1.22`, single `require`
  (`maxminddb-golang`); `go.sum`.
- `vendor/` — `go mod vendor` (run in the toolchain container), committed, so builds
  are offline.
- Directory skeleton from [project.md §5](project.md) (empty packages compile).
- `Dockerfile` (multi-stage) + `docker-compose.yml` (`network_mode: host`).
- **No `Makefile`, no Go on the host** — every toolchain command runs in the
  containerised pattern of [project.md §16](project.md).
- `.gitignore` — `/tavazon`, `*.mmdb`, `/data/state.json`, `/data/metering/`, `*.log`.
- `LICENSE` — MIT.

**Definition of done**

- [ ] `go build -mod=vendor ./...`, run in the toolchain container, succeeds offline.
- [ ] The toolchain-container build and test commands (project.md §16) run cleanly
      (no tests yet, but the commands work).
- [ ] Repo layout matches project.md §5; no `.github/` directory, no `Makefile` exists.

---

## Phase 1 — Data & I/O packages

*project.md §17 steps 2–5.*

The packages that read the outside world. GeoIP is here because targeting depends on it.

- `internal/config` — `Config` struct, `Load`, `Default`, `Validate`, `ApplyEnv`,
  `ApplyFlags`.
- `internal/netstat` — `/proc/net/dev` parser, `Read`, `Interfaces`; `parse()` split
  out for fixture testing.
- `internal/geoip` — MaxMind ASN+Country reader, the ASN→prefixes index, `ListASNs`,
  `Prefixes`, `LookupASN`, plus the in-memory `NewForTest` constructor (test seam).
- `internal/state` — `State` struct, atomic `Load`/`Save`, `PurgeExpired`, IP cache.

**Definition of done**

- [ ] Every package has the tests listed in [project.md §15](project.md).
- [ ] `go test -race ./internal/...` green.
- [ ] Config precedence (defaults → file → env → flags) is test-verified.
- [ ] Atomic state write leaves no partial file under crash simulation.
- [ ] `geoip.Open` resolves ASN, enumerates prefixes, and filters by country against
      the operator's real `maxmind_files/` databases (test skips if absent);
      `NewForTest` builds a known in-memory index. No `.mmdb` is committed.
- [ ] Flaws #2, #6, #7, #10, #16 from the [STANDARDS.md](STANDARDS.md) table addressed.

---

## Phase 2 — Scheduling & targeting

*project.md §17 steps 6–7.*

The pure-logic brain: how much to send, when, and where.

- `internal/schedule` — `Deficit` for ratio and volume modes; the continuous
  Catmull-Rom curve and the mean-reverting wander.
- `internal/targets` — ASN-weighted random IP/port generation over selected ASNs.

**Definition of done**

- [ ] Ratio and volume `Deficit` both correct, including the `min_deficit` cutoff and
      the volume window roll-over.
- [ ] The curve is **C1-continuous and periodic** — test asserts no step at any hour
      boundary and value at 24⁻ equals value at 0 (flaw #17).
- [ ] The curve can reach ~0 at the pre-dawn trough; wander stays bounded.
- [ ] `RandomTarget` covers every selected ASN, generates IPs inside their prefixes,
      ports inside `[port_min,port_max]`.
- [ ] Flaw #17 addressed.

---

## Phase 3 — Traffic engine & observability

*project.md §17 steps 8–10.*

The part that moves bytes, plus the metering and logging that record it.

- `internal/uploader` — `payload.go` (fully-random size + content), the token-bucket
  rate limiter, `RunCycle` (worker pool).
- `internal/metering` — time-series store, 5-minute bucketing, 95th-percentile calc,
  per-ASN accounting, daily rollups, the config-change audit log.
- `internal/metrics` — in-memory registry, `Snapshot`, Prometheus text.
- `internal/sysstat` — process CPU% / RAM / bandwidth sampler for the resource panel.
- `internal/logging` — `slog` setup + size-rotating file writer.

**Definition of done**

- [ ] Datagram sizes are uniform in `[min,max]`, never exceed `max_datagram`; payload
      bytes are non-constant (flaw #5).
- [ ] Token bucket delivers ≈ the configured rate over a measured window.
- [ ] `RunCycle` respects `max_workers`, is cancellable via `context`, and reports
      per-ASN bytes.
- [ ] Metering buckets to 5 minutes; 95th-percentile calc matches a known sample set;
      per-ASN totals and daily rollups are correct; audit append works.
- [ ] `sysstat` parses process CPU/RSS + system memory from `/proc`; metrics expose
      the CPU/RAM/bandwidth resource block.
- [ ] Log file rotates at `max_size_mb`, keeps `max_backups` (flaw #9).
- [ ] Flaws #5, #8, #9, #18 addressed.

---

## Phase 4 — Orchestration

*project.md §17 step 11.*

Wire Phases 1–3 into the cycle loop.

- `internal/engine` — `Run`, the loop of [project.md §6.11](project.md): netstat read,
  reboot detection / synchronizer, deficit (either mode), curve-derived & slew-limited
  worker count, `RunCycle`, metering sample, state save, randomized sleep.
  Run state is the persisted `general.running` flag, read live each cycle (no
  Pause/Resume method); the web layer's Start/Stop + Dev-mode buttons mutate it.

**Definition of done**

- [ ] Reboot detection re-anchors the synchronizer on *either* counter dropping (flaw #7).
- [ ] Each iteration is wrapped in `recover()`; a panicking cycle does not stop the loop.
- [ ] Deficit below `min_deficit_bytes` (ratio) or already-met budget (volume) skips
      the cycle; no hourly tick anywhere in the loop.
- [ ] Worker count is slew-limited between cycles and capped at `max_workers`.
- [ ] State saved atomically at end of every cycle; metering sampled every cycle.
- [ ] An engine-level test drives a fake `netstatFn` through reboot and steady-state.

---

## Phase 5 — Interface

*project.md §17 steps 12–13.*

The control + monitoring surface and the entrypoint.

- `internal/web` — HTTP server, REST API, control endpoints
  ([project.md §10](project.md)), `embed`-bundled `static/index.html`: the full
  dashboard — live stats, 24-hour per-second chart, billing panel (95th percentile +
  total volume), per-ASN breakdown, history view, settings form with ASN picker,
  settings-change history.
- `cmd/tavazon/main.go` — flag parsing, config/state/geoip/targets/metering/metrics
  wiring, web + engine startup, `signal.NotifyContext` graceful shutdown.

**Definition of done**

- [ ] All endpoints in project.md §10.1 implemented; `auth_token` enforced when set.
- [ ] Dashboard is one self-contained file — inline CSS/JS, **no CDN**, no Grafana
      (RED_LINES X6).
- [ ] ASN picker lists Iranian ASNs by default and applies the selection live.
- [ ] `PUT /api/config` validates, applies live, persists, and writes an audit record.
- [ ] Billing panel shows 95th-percentile rate and total volume; history view renders
      days/months from `/api/history`.
- [ ] `-print-config` and `-version` work; flag precedence holds (project.md §12).
- [ ] SIGINT/SIGTERM finishes the current cycle, flushes metering, saves state,
      flushes logs, exits 0.

---

## Phase 6 — Deployment & smoke test

*project.md §17 steps 14–15.*

Make it shippable and prove it end to end.

- `Dockerfile` (multi-stage, `scratch` final, `-mod=vendor`), `docker-compose.yml` (host
  networking, no Redis), `systemd/tavazon.service`.
- `README.md` — install, **how to obtain the GeoLite2 `.mmdb` files**, usage.
- `config.example.json`.
- End-to-end smoke test against a local UDP sink.

**Definition of done**

- [ ] `docker build` produces a small static image; container runs with no Redis and
      reads `.mmdb` files from the mounted volume.
- [ ] The `cross` command (project.md §16) yields working amd64 + arm64 static
      binaries, built offline in the toolchain container.
- [ ] README clearly explains GeoLite2 download + placement (files never committed).
- [ ] E2E smoke test passes: bytes received, `fake_bytes_total` advanced, metering
      recorded the ASN.
- [ ] All 18 flaws in the STANDARDS.md table signed off.

---

## Cross-cutting exit criteria

These hold at the end of **every** phase, not just Phase 6:

- In the toolchain container (project.md §16): `gofmt -l .` empty; `go vet` clean;
  `go test -race` green; `go build` succeeds offline (`-mod=vendor`).
- No dependency added beyond the allowlist; no `.mmdb` committed beyond the test fixture.
- Nothing in [RED_LINES.md](RED_LINES.md) is crossed.
- No periodic/modal/constant pattern introduced into generated traffic
  ([STANDARDS.md §9](STANDARDS.md)).
- project.md stays the source of truth — if code diverges, project.md is updated in
  the same change.
