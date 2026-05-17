# Phase 5 — Interface

> Plan for [ROADMAP.md](../ROADMAP.md) Phase 5. The control + monitoring surface and
> the real entrypoint.

**Delivers:** `internal/web` (HTTP API + embedded dashboard), the full
`cmd/tavazon/main.go`.
**Depends on:** Phase 1–4 (everything).
**Unblocks:** Phase 6 (deployment).

Build `web` first, then `main.go`.

---

## 5.1 `internal/web` — `web.go`

Implements [project.md §10](../project.md). Pure stdlib `net/http`.

**Type:**

```go
type Server struct {
    cfg      *configHolder        // RWMutex-guarded current config + setter
    state    *state.State
    engine   *engine.Engine
    metrics  *metrics.Registry
    metering *metering.Store
    geoip    *geoip.GeoIP
    log      *slog.Logger
}
```

**Functions:**

- `New(...) *Server`; `(*Server) Handler() http.Handler` — a `*http.ServeMux` with
  the routes below; `Run(ctx) error` — `http.Server` with graceful `Shutdown` on
  `ctx` cancel.
- `auth(next http.Handler) http.Handler` — middleware: if `cfg.Web.AuthToken` is set,
  require it on `/api/*` and `/metrics` as `Authorization: Bearer` or `?token=`;
  `/healthz` is always open.

**Routes** (project.md §10.1) — one handler each:

| Route | Handler does |
|-------|--------------|
| `GET /` | serve the embedded `index.html` |
| `GET /api/stats` | `metrics.Snapshot()` + curve intensity + per-ASN cycle, as JSON |
| `GET /api/config` | current config JSON |
| `PUT /api/config` | decode → `Validate` → apply live (RWMutex) → persist to `config.json` → `metering.AppendAudit` with the diff |
| `GET /api/asns?country=` | `geoip.ListASNs(country)` |
| `GET /api/history?from=&to=&granularity=` | `metering.History(...)` |
| `GET /api/billing` | `metering.Percentile()` — 95th pct + total volume |
| `GET /api/audit?n=` | `metering.Audit(n)` |
| `POST /api/control/start` / `stop` | flip `general.running`, persist `config.json`, `metering.AppendAudit` — engine reads it next cycle |
| `POST /api/control/dev` | body `{enabled}` → flip `dev.enabled`, persist `config.json`, audit |
| `POST /api/control/reset-counters` | zero tracked totals, re-anchor sync to raw |
| `POST /api/control/set-sync` | body `{upload_gb,download_gb}` → set lifetime totals |
| `GET /api/logs?n=` | tail N lines of the log file |
| `GET /metrics` | `metrics.Prometheus()` |
| `GET /healthz` | `200 ok` |

Every handler: decode defensively, return JSON errors with proper status codes, never
panic out (recover middleware as a backstop).

## 5.2 `internal/web/static/index.html`

Implements [project.md §10.3](../project.md). **One self-contained file** — inline CSS,
inline vanilla JS, **no CDN, no framework, no build step**. Embedded via `//go:embed`.

Sections to build (each just `fetch()`es a JSON endpoint on a timer and repaints):

- header (uptime, **Start/Stop** toggle button, **Dev-mode** toggle button, mode,
  connection dot; a `DEV MODE → <dev.target>` banner whenever dev mode is on);
- stat cards (Σ up/down, ratio colour-coded, ⇧/⇩ speed, workers, deficit, curve
  intensity);
- 24-hour per-second `<canvas>` chart, hand-drawn from the `speed_samples` ring;
- resource panel — three hand-drawn CPU / RAM / Bandwidth pie/donut gauges, from
  `resources` in `/api/stats`;
- billing panel — 95th-percentile rate (⇧/⇩) + total volume + per-ASN split;
- per-ASN breakdown table/bars;
- history view — charts from `/api/history` over days/months;
- settings form bound to `/api/config` incl. the **ASN picker** (multi-select with
  search, populated from `/api/asns?country=IR`) and inputs for every tunable
  constant — `base_rate_bps`, `cycle_budget_fraction`, `packet_gap_max`,
  `max_workers`, `max_ramp`, datagram min/max, `link_capacity_mbit`, `dev.target`,
  `dev.workers` — each enforcing its `Validate` minimum; "Apply" → `PUT`;
- settings-change history from `/api/audit`;
- counter tools (reset / set-sync) behind confirm dialogs;
- log tail from `/api/logs`.

**`web_test.go`:** `httptest.Server` over `Handler()`: each route returns the right
status + JSON shape; `auth` rejects a missing/wrong token when one is configured and
allows `/healthz` regardless; `PUT /api/config` with bad input is rejected and writes
no audit record; a good `PUT` applies, persists, and audits.

## 5.3 `cmd/tavazon/main.go`

Implements [project.md §7.1, §12](../project.md). Replaces the Phase 0 stub. **Wiring
only — no business logic.**

- Define the flags of project.md §12 (`-config -state -asn-db -country-db -listen
  -mode -multiplier -stopped -no-web -log-level -print-config -version`).
- `config.Load` → `ApplyEnv` → `ApplyFlags` → `Validate`. `-print-config` prints the
  merged config and exits; `-version` prints and exits.
- `logging.Setup`; `state.Load`; `geoip.Open` (on failure: log, keep going so the
  dashboard shows the problem, but the uploader stays idle); `targets.New`;
  `metering.Open`; `metrics` registry; `uploader`, `schedule.Curve`, `engine.New`.
- `signal.NotifyContext(SIGINT, SIGTERM)`; start `web.Run` in a goroutine if enabled;
  run `engine.Run` in the main goroutine.
- On signal: cancel ctx → engine finishes its cycle → flush metering → save state →
  flush logs → exit 0.
- Wrap the web goroutine in `defer recover()` (RED_LINES X4).

**Manual smoke check** (real e2e test is Phase 6):

```sh
docker compose run --rm tavazon -print-config
```

---

## Definition of done

- [ ] `go test -race ./internal/web` green.
- [ ] Every project.md §10.1 route works; `auth_token` enforced when set.
- [ ] Dashboard is one embedded file — no CDN, no Grafana; ASN picker works live.
- [ ] Start/Stop and Dev-mode are header toggle buttons; each persists to
      `config.json`, is audited, and survives a restart. Dev banner shows when on.
- [ ] `PUT /api/config` validates, applies live, persists, and audits.
- [ ] Billing panel shows 95th pct + total volume; history renders days/months.
- [ ] `-print-config`/`-version` work; flag precedence holds; SIGTERM shuts down
      cleanly (cycle finished, metering flushed, state saved, exit 0).
- [ ] `gofmt`/`vet` clean; build offline.
