# Tavazon — Design Document

> **Tavazon** (Persian: توازن — *balance / equilibrium*) is a ground-up Go rewrite of
> [namizun](https://github.com/malkemit/namizun). It restores the upload/download
> *balance* that Iranian hosting providers break with their asymmetric quota policy.
>
> This document specifies **every** piece of the project so it can be built from
> scratch without referring back to the original repository. It is the **source of
> truth**: if code ever diverges from it, this document is updated in the same change.
>
> Companion documents: [RED_LINES.md](RED_LINES.md) (inviolable constraints),
> [STANDARDS.md](STANDARDS.md) (engineering standards), [ROADMAP.md](ROADMAP.md)
> (phased build path), [CONTRIBUTING.md](CONTRIBUTING.md) (workflow).

---

## Table of Contents

1. [Background — what the original does](#1-background)
2. [Why rewrite — flaws of the original](#2-why-rewrite)
3. [Goals & non-goals](#3-goals--non-goals)
4. [Technology decisions](#4-technology-decisions)
5. [Repository layout](#5-repository-layout)
6. [Core algorithms (the heart of the project)](#6-core-algorithms)
7. [Package-by-package specification](#7-package-by-package-specification)
8. [Configuration schema](#8-configuration-schema)
9. [Persistent state & metering store](#9-persistent-state--metering-store)
10. [Web dashboard & HTTP API](#10-web-dashboard--http-api)
11. [Metrics & logging](#11-metrics--logging)
12. [CLI interface](#12-cli-interface)
13. [Concurrency model](#13-concurrency-model)
14. [Deployment artifacts](#14-deployment-artifacts)
15. [Testing strategy](#15-testing-strategy)
16. [Build](#16-build)
17. [Implementation order](#17-implementation-order)
18. [Legal & ethical note](#18-legal--ethical-note)

---

## 1. Background

### 1.1 The problem

Iranian hosting/transit providers enforce an **asymmetric traffic quota**. A server is
sold with, for example, a 1:8 ratio: for every 1 GB the server *downloads* (ingress),
it is *allowed* to *upload* (egress) up to 8 GB — but if download exceeds its share,
the account is throttled or suspended. In practice the scarce, expensive direction is
**download**. Operators of proxies/VPNs (used to bypass national censorship) burn
download quota fast and run out long before their upload quota.

Billing on the backbone link is itself two-pronged: providers commonly charge on the
**95th-percentile rate** of 5-minute samples *and* on **total transferred volume**.
Tavazon must therefore not only fix the ratio but let the operator *see* both billing
quantities — see §6.10.

### 1.2 The original solution (namizun)

namizun keeps the ratio "healthy" by **manufacturing fake outbound (upload) traffic**:
it sends junk UDP datagrams to **random Iranian IP addresses**. Those datagrams are
dropped at the destination (no service listening, payload meaningless), so nothing
useful is transferred — but the server's NIC egress counter, which the provider bills
against, goes up. By inflating *upload*, the *download* head-room is preserved.

The traffic is intentionally:

- **Distributed** across thousands of destination IPs (so it is not a DoS on any one
  host — each IP receives only a small trickle).
- **Low-volume per IP** and **rate-limited**.
- **Shaped by time of day** so it looks organic (a real proxy server is busy in the
  evening and nearly idle at 4–5 AM).

### 1.3 How the original is structured (for reference)

The original is Python, split into two pip packages plus a Redis dependency:

- `namizun_core/` — engine: `database.py` (Redis wrapper), `ip.py` (random IP/port),
  `udp.py` (the UDP sender), `network.py` (psutil counters), `time.py`, `log.py`.
- `namizun_menu/` — a recursive terminal UI with a live monitoring table.
- `uploader.py` — the daemon loop (run under systemd as `namizun.service`).
- `else/` — `setup.sh` installer, `range_ips` (a static IP-range list), `namizun.service`.

State lived in Redis; targets were drawn from a **static, hand-maintained IP-range
file**. Tavazon discards both: targets come from a live MaxMind GeoIP database keyed by
**AS number** (§6.5), and all state persists to local files (§9).

---

## 2. Why rewrite

The *ideas* in namizun are sound. The *implementation* has structural problems that a
rewrite should fix:

| # | Problem in the original | Consequence |
|---|---|---|
| 1 | `namizun_menu/__init__.py` imports `menu` three times — name collision | Only `udp_submenu.menu` is reachable via the package |
| 2 | Invalid "speed" input calls the *threads-count* setter (copy/paste bug) | Wrong menu shown |
| 3 | **No exception handling anywhere** | A transient Redis/network error kills worker threads silently or crashes the daemon |
| 4 | **Recursion used for control flow** — every menu navigation and retry recurses | A long session grows the call stack without bound → eventual `RecursionError` |
| 5 | UDP payload is `bytes(n)` = **all zero bytes**, constant size buckets | Trivially fingerprintable by provider DPI as fake traffic |
| 6 | `psutil.net_io_counters()` sums **all** interfaces incl. loopback | Loopback / VPN-tunnel traffic pollutes the measurement |
| 7 | Reboot detection only triggers when **both** counters drop | A one-sided counter reset is missed |
| 8 | **One OS thread per target IP** under the GIL | Wastes CPU; the README has to tell users to buy more cores |
| 9 | Log file is opened/closed on **every line**; `namizun.log` never rotates | I/O overhead; unbounded disk growth |
| 10 | **No headless configuration** — config only settable through the interactive TUI | The Docker `uploader` service cannot actually be configured |
| 11 | Hardcoded absolute paths `/var/www/namizun` everywhere | Not portable |
| 12 | Module-global mutable state shared across threads in `udp.py` | Not reentrant; fragile |
| 13 | No tests, no type discipline, no quality gates | Unsafe to change |
| 14 | Hard dependency on an external Redis server | Extra moving part for a single-host tool |
| 15 | Tri-state `in_submenu` flag polled through Redis to coordinate two TUI threads | Clunky IPC, terminal-cursor race conditions |
| 16 | **Static, hand-maintained IP-range list** that ages out of date | Targets drift from reality; no control over *which* networks are hit |
| 17 | **Stepwise hourly traffic curve** — worker count jumps on the hour | The on-the-hour step is visible to the naked eye in a traffic graph → easy to flag |
| 18 | **No history / metering** — only a live TUI table, nothing persisted | Operator cannot see what was sent over days/months, nor the 95th-percentile bill |

### Ideas worth **keeping**

- The **reboot-aware synchronizer** that preserves lifetime totals across restarts.
- A **time-of-day traffic shape** (kept — but made *continuous*, see §6.4).
- **Distributed, low-volume-per-IP** targeting (this is what keeps it from being a DoS).
- **TTL-churned IP cache** so the destination set is never static.
- The **deficit formula** `download × ratio − upload`.

---

## 3. Goals & non-goals

### Goals

- **Single binary, offline build.** `scp` the binary, run it. Build needs no network:
  the one external dependency is **vendored** (`vendor/` committed) — see §4.
- **Headless-first.** Fully configurable via a JSON file + CLI flags + env vars. The
  recursive TUI is gone; a **web dashboard** replaces it.
- **No mandatory external services.** State and history persist to local files. No
  Redis, no database server, no Grafana.
- **ASN-targeted.** The operator chooses *which AS numbers* the fake traffic goes to;
  destination IPs are resolved live from a MaxMind GeoIP database (§6.5).
- **Robust.** Every goroutine has panic recovery; every error is logged and survived.
- **Observable over the long run.** A self-contained dashboard shows live state,
  editable settings, a settings-change history, per-ASN traffic accounting, a 24-hour
  per-second traffic chart, the **95th-percentile rate**, and **total volume** — over
  days and months, not just a live snapshot.
- **DPI-resistant by nature.** Because the payload is junk, it follows *no* real
  protocol; every datagram is fully randomised in size, content, destination port, and
  timing, so there is no packet fingerprint to match (§6.9).
- **Smooth, organic shaping.** The traffic curve is a continuous function of time with
  slow random wander — no on-the-hour step, nothing periodic enough to eyeball.
- **Tested.** Unit tests for every pure-logic package.

### Non-goals

- Not a load-testing or DoS tool. Per-IP volume stays small and distributed.
- No Windows/macOS daemon support — Linux only (it reads `/proc/net/dev`). The binary
  still *builds* cross-platform; only the netstat reader is Linux-specific.
- No clustering / multi-host coordination. One binary = one host.
- IPv4 only. IPv6 prefixes in the GeoIP data are skipped; the server's v4 path is what
  the provider's quota meters.
- No CI/CD, no `Makefile`, no Go toolchain on the host. Go runs **only inside a
  container** with the working tree bind-mounted (§16); quality gates are the
  [CONTRIBUTING.md](CONTRIBUTING.md) pre-push checklist. The repo carries no
  `.github/` workflows.
- No Grafana / Prometheus-stack integration. A Prometheus `/metrics` endpoint is
  *offered* for those who want it, but the primary UI is the built-in dashboard.

---

## 4. Technology decisions

| Decision | Choice | Rationale |
|---|---|---|
| Language | **Go 1.22+** | Single binary, real concurrency without a GIL, excellent `net` stdlib, easy cross-compile |
| Module path | `namizungo` | — |
| Dependencies | **One, curated & vendored:** `github.com/oschwald/maxminddb-golang` (+ its transitive closure) | Reading MaxMind `.mmdb` is the one thing not worth re-implementing. Everything else is stdlib. The dependency is **vendored** so `go build -mod=vendor` works fully offline behind a censored network. The allowlist is closed — see [RED_LINES.md](RED_LINES.md) X1 |
| GeoIP source | **MaxMind GeoLite2-ASN + GeoLite2-Country** `.mmdb` | ASN db maps IP↔ASN; Country db lets the dashboard pre-filter the ASN picker to Iran. Files are **operator-supplied and never committed** (MaxMind EULA forbids redistribution) |
| Config format | **JSON** (`config.json`) | Zero extra dependency; a documented `config.example.json` ships with the repo |
| State store | **Local JSON file**, atomic write | Synchronizer offsets + IP cache. Tiny, written once per cycle |
| Metering store | **Local append-only files** under `data/metering/` | Time-series history, per-ASN accounting, config-change audit. Custom flat-file format — no database server, no Grafana (§9) |
| UI | **Embedded web dashboard** + JSON API | Replaces the recursive TUI; reachable remotely; `embed`-bundled so still one binary |
| Logging | `log/slog` (stdlib) with a **size-rotating** file handler we write ourselves | Structured logs, bounded disk usage |
| Process management | systemd unit **and** Docker image, both provided | — |
| Timezone | `Asia/Tehran` via `time.LoadLocation`; tzdata embedded with `import _ "time/tzdata"` | The curve is in Iran local time; embedding the zone DB (~450 KB) means it works on a `scratch` image that ships no system zoneinfo |

> **Cross-compilation:** `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build` and likewise
> `arm64`. `maxminddb-golang` is pure Go, so the binary stays fully static and CGO-free.

---

## 5. Repository layout

```
tavazon/
├── go.mod                       # module namizungo, go 1.22
├── go.sum                       # checksums for the one vendored dependency
├── vendor/                      # vendored deps — COMMITTED, enables offline build
├── README.md                    # user-facing install + usage
├── LICENSE                      # MIT
├── Dockerfile                   # multi-stage: build stage IS the build env; scratch final image
├── docker-compose.yml           # runtime service, network_mode: host, no Redis
├── .gitignore                   # /tavazon, *.mmdb, /data/state.json, /data/metering/, *.log
├── config.example.json          # reference config, documented by the README
├── cmd/
│   └── tavazon/
│       └── main.go              # entrypoint: flag parsing, wiring, signal handling
├── internal/
│   ├── config/
│   │   ├── config.go            # Config struct, Load(), Validate(), defaults
│   │   └── config_test.go
│   ├── netstat/
│   │   ├── netstat.go           # /proc/net/dev parser, per-interface counters
│   │   └── netstat_test.go      # parses a fixture file
│   ├── geoip/
│   │   ├── geoip.go             # MaxMind ASN+Country reader, ASN→prefixes index
│   │   └── geoip_test.go        # Open() vs the real maxmind_files/; NewForTest seam
│   ├── state/
│   │   ├── state.go             # State struct, atomic Load/Save, IP cache
│   │   └── state_test.go
│   ├── schedule/
│   │   ├── schedule.go          # ratio & volume target modes; the deficit/budget math
│   │   ├── curve.go             # continuous 24h traffic curve + random-walk jitter
│   │   └── schedule_test.go
│   ├── targets/
│   │   ├── targets.go           # ASN-based random IP/port generation
│   │   └── targets_test.go
│   ├── uploader/
│   │   ├── uploader.go          # worker pool, token-bucket limiter, UDP send loop
│   │   ├── payload.go           # fully-random payload + size generation
│   │   └── uploader_test.go     # tests payload/size logic (no real sockets)
│   ├── metering/
│   │   ├── metering.go          # time-series store, per-ASN accounting, rollups
│   │   ├── percentile.go        # 95th-percentile-of-5-min-samples billing calc
│   │   ├── audit.go             # append-only config-change audit log
│   │   └── metering_test.go
│   ├── engine/
│   │   └── engine.go            # the orchestration loop tying everything together
│   ├── metrics/
│   │   └── metrics.go           # in-memory counters, snapshot, Prometheus text
│   ├── sysstat/
│   │   ├── sysstat.go           # process CPU% + RAM (RSS) + system mem, from /proc
│   │   └── sysstat_test.go      # parses checked-in /proc fixtures
│   ├── logging/
│   │   └── logging.go           # slog setup + size-rotating file writer
│   └── web/
│       ├── web.go               # HTTP server, REST API, control endpoints
│       └── static/
│           └── index.html       # self-contained dashboard (HTML+CSS+JS, no CDN)
├── data/                        # runtime data: state + metering — contents gitignored
│   └── .gitkeep
├── maxmind_files/                # operator-supplied GeoLite2 .mmdb files — gitignored
│   └── .gitkeep
└── systemd/
    └── tavazon.service          # systemd unit file
```

`internal/` is used so nothing is importable by third parties — this is an application,
not a library. There is **no `.github/` directory** — see §16. The `data/` and
`maxmind_files/` directories are committed empty (`.gitkeep`); the operator drops the
two GeoLite2 `.mmdb` files into `maxmind_files/`, and state + metering files are
created under `data/` at runtime — all gitignored.

---

## 6. Core algorithms

This section is the technical heart. All pseudocode is illustrative Go.

### 6.1 Reading network counters

`netstat` reads `/proc/net/dev`. Each line after the two header lines is:

```
  eth0: 1234567890 12345 0 0 0 0 0 0  9876543210 54321 0 0 0 0 0 0
        └─rx bytes┘ ...                └─tx bytes┘ ...
```

Field order: interface name, then 8 RX fields, then 8 TX fields.

```go
type Counters struct {
    TxBytes uint64 // egress  = "upload"
    RxBytes uint64 // ingress = "download"
}

// Read returns counters summed over the selected interfaces.
//   - if iface == "" : sum every interface whose name is not "lo" and is "up"
//   - otherwise      : only that named interface
func Read(iface string) (Counters, error)
```

Loopback (`lo`) is **always excluded** when auto-summing — fixing flaw #6. A helper
`parse(r io.Reader, iface string)` is split out so tests feed a fixture.

### 6.2 Reboot detection & the synchronizer

The provider bills **lifetime** egress. The kernel's `/proc` counters reset to ~0 on
every reboot. To present a continuous lifetime total we keep a **synchronizer offset**
added to the raw kernel counter:

```
trackedUpload   = rawTx + uploadSync
trackedDownload = rawRx + downloadSync
```

Both `*Sync` values and the last-known `tracked*` totals are persisted in state.

Each engine cycle:

```go
raw := netstat.Read(cfg.Network.Interface)          // rawTx, rawRx
liveUpload   := raw.TxBytes + state.UploadSync
liveDownload := raw.RxBytes + state.DownloadSync

rebooted := liveUpload < state.TotalUpload || liveDownload < state.TotalDownload
if rebooted {
    // counters went backwards => machine rebooted (or counters wrapped).
    // Re-anchor the synchronizer so the tracked total is unchanged.
    state.UploadSync   = state.TotalUpload   - raw.TxBytes
    state.DownloadSync = state.TotalDownload - raw.RxBytes
    log "reboot detected, synchronizer re-anchored"
} else {
    state.TotalUpload   = liveUpload
    state.TotalDownload = liveDownload
}
```

**Improvement over the original:** the original required *both* counters to drop
(flaw #7). Tavazon triggers on *either*. `*Sync` values are `int64` (they can be
negative — e.g. after a manual counter reset).

### 6.3 Target modes — how much to send

Tavazon supports **two target modes**; the active one is `target.mode` in config and is
switchable live from the dashboard.

**Ratio mode (default).** Keep `upload = download × multiplier`. `multiplier` defaults
to **8** and is editable from the dashboard.

```go
// jitter the configured multiplier by ±jitterFraction each cycle so the
// generated traffic does not look mathematically perfect.
effMul  := cfg.Target.Ratio.Multiplier *
           (1 + randFloat(-cfg.Target.Ratio.Jitter, +cfg.Target.Ratio.Jitter))

deficit := int64(float64(trackedDownload)*effMul) - int64(trackedUpload)
if deficit < cfg.Target.Ratio.MinDeficitBytes {   // default 1 GiB
    deficit = 0
}
```

`multiplier` is the target download:upload ratio. If the provider grants 1:8, leave it
at 8. A deficit ≤ `MinDeficitBytes` means we are already balanced enough; skip.

**Volume mode.** Push a fixed budget — *N* bytes over a window of *M* hours
(e.g. `1G / 24h`, `2G / 6h`). Independent of download.

```go
// fraction of the window's curve-weighted budget that should be spent by now,
// minus what has already been sent in this window.
scheduled := volumeBudgetUpTo(now)            // see §6.4 — curve-weighted
deficit   := scheduled - state.WindowSentBytes
```

`volumeBudgetUpTo(now)` integrates the continuous curve (§6.4) from the window start to
`now` and scales it so the integral over the whole window equals exactly *N* bytes. The
window then rolls over and repeats. This makes a volume target track the same organic
day-shape as ratio mode instead of a flat line.

Both modes feed a single non-negative `deficit` (bytes to manufacture soon) into the
engine; everything downstream is mode-agnostic.

### 6.4 The continuous 24-hour traffic curve

A real proxy server is busy in the evening and idle pre-dawn. Tavazon shapes intensity
by the **Tehran-local time of day** — but as a **continuous function**, not a per-hour
step. Flaw #17 (the visible on-the-hour jump) is fixed here.

**Anchors.** Config carries 24 anchor values `anchors[0..23]`, one per hour, each a
relative intensity (≈ 0 idle … ≈ 2 peak). Defaults ship with a plausible day-shape
(low at 04:00–05:00, rising through the evening).

**Continuous interpolation.** Intensity at a fractional hour `h ∈ [0,24)` is a
**periodic Catmull-Rom spline** through the 24 anchors:

```go
// catmullRom interpolates smoothly (C1-continuous) and periodically through
// the 24 anchor points; the curve wraps 23->0 with no discontinuity.
func curveBase(h float64) float64   // h in [0,24)
```

Because the spline is C1-continuous and periodic, the intensity has **no step and no
seam** anywhere in the day — sampling it once per engine cycle yields a smooth shape.

**Slow random wander.** On top of the deterministic spline runs a bounded, smooth,
mean-reverting random walk (an Ornstein-Uhlenbeck process) `wander(t)`, so no two days
are identical and the curve never looks mechanically periodic:

```go
intensity(t) = clamp( curveBase(hourOf(t)) * (1 + wander(t)), 0, curveMax )
```

`wander` changes slowly (configurable reversion + strength); it never jumps.

**Applying it.** The engine converts `intensity` into a target worker count and a
target send rate, and **slew-limits** changes between cycles so even at cycle
granularity the realised traffic ramps rather than steps:

```go
base    := cfg.Uploader.ThreadsCoefficient * 10
target  := int(intensity(now) * float64(base))
workers := slewToward(prevWorkers, target, cfg.Uploader.MaxRamp)   // smooth ramp
workers  = min(workers, cfg.Uploader.MaxWorkers)                    // hard cap
```

At the pre-dawn trough the curve can reach `0` — Tavazon goes fully idle, exactly like
a sleeping user's server. The anchors, `curveMax`, and the wander parameters are all
overridable in config (§8).

### 6.5 GeoIP — ASN & country resolution

`internal/geoip` wraps the MaxMind reader. On startup it opens both `.mmdb` files and
builds an in-memory **ASN index**:

```go
type GeoIP struct { ... }

// Open loads GeoLite2-ASN.mmdb and GeoLite2-Country.mmdb from disk.
func Open(asnPath, countryPath string) (*GeoIP, error)

// NewForTest builds the same ASN index from in-memory maps — no file I/O.
// It lets targets/engine/e2e tests run with no .mmdb on disk at all.
func NewForTest(prefixes map[uint32][]net.IPNet, country map[uint32]string) *GeoIP

// ASNInfo describes one autonomous system discovered in the databases.
type ASNInfo struct {
    Number   uint32   // AS number
    Name     string   // AS organisation name
    Country  string   // ISO country of the bulk of its prefixes ("IR", ...)
    Prefixes []net.IPNet
    NumIPs   uint64   // total addressable IPs across its prefixes
}

func (g *GeoIP) ListASNs(country string) []ASNInfo // "" = all; "IR" = Iran only
func (g *GeoIP) Prefixes(asn uint32) []net.IPNet
func (g *GeoIP) LookupASN(ip net.IP) (uint32, bool)
```

The index is built by walking the ASN database's network tree once
(`maxminddb.Networks()`), recording each prefix under its ASN, and tagging each ASN
with the country its prefixes mostly fall in (looked up in the Country db). Only
**IPv4** prefixes are indexed — IPv6 networks are skipped (Tavazon is IPv4-only for
v1). The dashboard's ASN picker calls `ListASNs("IR")` so the operator sees **Iranian ASNs**
first, but any ASN remains selectable.

The `.mmdb` files live in `maxmind_files/`, are **operator-supplied** (downloaded from
MaxMind) and **never committed**. If they are missing at startup, Tavazon logs a clear error telling the
operator where to put them and refuses to start the uploader (the dashboard still comes
up so the problem is visible).

### 6.6 Target IP & port generation

`internal/targets` turns the operator's **selected ASN set** into concrete destinations.

```go
type Targets struct {
    pools []asnPool  // one per selected ASN: its prefixes + IP-count weight
    rng   *rand.Rand
}
func New(g *geoip.GeoIP, selectedASNs []uint32, cfg TargetsConfig) (*Targets, error)
func (t *Targets) RandomTarget() (ip net.IP, port int, asn uint32)
```

`RandomTarget`:

1. Pick a selected ASN, **weighted by its total IP count** so larger networks are not
   under-represented (but every selected ASN is still reachable).
2. Pick one of that ASN's prefixes (weighted by prefix size).
3. Pick a uniformly random host address inside that prefix.
4. Pick a **uniformly random destination port** in `[port_min, port_max]`
   (default `1024–65535`). No fixed port list — pure randomness (§6.9, flaw #5).

The returned `asn` is threaded through to metering so per-ASN accounting (§6.10) knows
where each byte went. If the operator has selected no ASNs, the uploader stays idle and
the dashboard shows a "select at least one ASN" notice.

**Dev mode.** When `dev.enabled` is true (§8), the uploader (§6.8) bypasses
`RandomTarget` and sends every datagram to the single operator-configured `dev.target`
IP — random port as usual, `asn = 0` (metered under a synthetic "DEV" label). `targets`
itself stays ASN-pure; the dev branch lives in the uploader, which already reads live
config. This sends the junk to a
**local** address (e.g. `192.168.1.1` on the operator's own LAN), so the egress shows
up on the real NIC tx counter and the *whole* pipeline — deficit, curve, workers,
metering, dashboard charts — is visible **without a VPS and without any real ASN
traffic**. Dev mode is off by default; it deliberately concentrates on one IP and is a
testing aid only — see [RED_LINES.md](RED_LINES.md) E1 (dev-mode exception). The
token-bucket rate limit (§6.8) still applies; worker count comes from `dev.workers`.

Distribution is mandatory: selection always spreads across many prefixes and IPs within
the chosen ASNs — see [RED_LINES.md](RED_LINES.md) E1.

### 6.7 The IP cache (TTL churn)

To avoid hammering a fixed set of IPs, chosen `(ip, port, asn)` triples are remembered
with an expiry. `RandomTarget` consults the cache in `state`:

```
if the cache has a non-expired entry:
    pop one at random, return it          // reuse
else:
    generate a fresh random target
    store it with expiry = now + rand(ttlMin, ttlMax)
    return it
```

Default TTL bounds: 10 min – 100 min. Expired entries are purged lazily on access and
during the periodic state save. The cache lives inside `state` so it survives restarts.

### 6.8 The worker pool & rate limiter

Replaces thread-per-IP (flaw #8). One cycle of uploading:

```
budget       := deficit × cfg.Uploader.CycleBudgetFraction   (default 0.3; §8)
workerCount  := from the continuous curve, slew-limited & capped   (§6.4)
perWorker    := budget / workerCount, jittered ±20% per worker

spawn exactly workerCount goroutines (a sync.WaitGroup), each:
    ip, port, asn := pickTarget()  // dev.target in dev mode (§6.6), else targets.RandomTarget()
    conn          := net.DialUDP(ip:port)
    sent          := 0
    for sent < thisWorkerQuota and ctx not cancelled:
        size := payload.RandomSize(cfg)            // §6.9 — uniform random
        buf  := payload.Random(size)               // §6.9 — random bytes
        rateLimiter.WaitN(size)                     // shared token bucket
        randomSleep(0, cfg.PacketGapMax)            // §6.9 — random inter-packet gap
        n, _ := conn.Write(buf)                     // errors logged, loop continues
        sent += n
        metrics.AddFakeBytes(n)
        metering.RecordSend(asn, n)                 // §6.10
    conn.Close()
wait for all workers, then return bytes actually sent
```

**Rate limiter** — a shared **token bucket**: refill rate
`R = base_rate_bps × speed_coefficient` bytes/sec, where `base_rate_bps` (default
512 KiB/s) and `speed_coefficient` (1–5) are both config fields — dashboard-editable,
each with a `Validate`-enforced minimum (§8). `WaitN(size)` blocks until `size`
tokens are available, smoothing total egress and bounding CPU.

**Bounded parallelism:** `workerCount` is capped by `cfg.Uploader.MaxWorkers`
(default 300) regardless of what the curve produces. The whole cycle runs under a
`context.Context` so `SIGTERM` or a dashboard "Stop" cancels in-flight workers cleanly.

### 6.9 Payload generation — pure randomness

`internal/uploader/payload.go`. The design principle, stated by the project owner: the
payload is **junk**, so it is bound by **no real protocol**. We exploit that fully —
there is nothing for DPI to fingerprint because nothing is ever the same twice.

- **Size.** Every datagram's length is drawn **uniformly at random** in
  `[min_datagram, max_datagram]` (default `64 … 1472`). There are no preferred size
  buckets, no weighted distribution — a uniform spread has no modal fingerprint.
  `max_datagram` defaults to **1472** (MTU 1500 − 20 IP − 8 UDP) so datagrams are
  **never IP-fragmented** — fragmentation is itself a fingerprint, and a too-large
  size was an outright bug in the original (could exceed the 65507-byte UDP limit).
- **Content.** Every datagram is filled with fresh pseudo-random bytes from a fast PRNG
  (`math/rand/v2` — speed, not crypto). No zero fill (flaw #5), no static header, no
  protocol-shaped bytes. High-entropy random content carries no signature.
- **Destination port.** Uniformly random per packet (§6.6) — not a fixed game-port set.
- **Timing.** The inter-packet gap is randomised (small random sleep) on top of the
  token bucket, so the *temporal* pattern is as noisy as the payload.

The result: no two datagrams share size, content, port, or precise timing. There is no
constant for a DPI signature to lock onto — the traffic is indistinguishable-by-nature
from arbitrary noise. We deliberately do **not** mimic a real protocol; imitation is
matchable, randomness is not.

> Removed from the original design: the all-zero payload, the weighted size-bucket
> distribution, the `buffer_coefficient` knob, the fixed game-port list, and the
> optional decoy protocol header. Pure randomness supersedes all of them.

### 6.10 Metering, 95th percentile & history

`internal/metering` is the long-horizon record the original entirely lacked (flaw #18).
It exists so the operator can answer, days or months later, *what was sent, where, and
what will the provider bill*.

**What is sampled.** Every engine cycle, metering records, from the **real interface
counters** (not just Tavazon's fake bytes — the provider bills the whole NIC):

- upload & download **rate** (bytes/sec, from the counter delta over the cycle),
- upload & download **volume** (cumulative),
- **fake bytes** Tavazon generated, broken down **per ASN**.

**Three retention tiers** (all local files under `data/metering/`, §9):

| Tier | Granularity | Retained | Used for |
|---|---|---|---|
| Live | per **second** | rolling 24 h, in memory (+ snapshot on shutdown) | the dashboard's 24-hour per-second chart |
| Mid  | per **5 minutes** | rolling ~13 months | the 95th-percentile billing calc |
| Long | per **day** | indefinitely (tiny) | months/years history view |

**95th-percentile billing.** Datacenters bill backbone links on the 95th percentile of
**5-minute** average-rate samples over the billing window (typically a calendar month):
sort all samples for the window, discard the top 5%, the next-highest sample is the
charged rate. `percentile.go` computes this for upload and download from the mid tier.
The dashboard shows the current-window 95th-percentile rate **and** the total
transferred volume — the two quantities the provider charges on — plus the per-ASN
split of the fake portion.

**Per-ASN accounting.** `RecordSend(asn, bytes)` accumulates fake bytes per ASN into the
mid/long tiers, so the dashboard can show *how much went to which AS* over any period.

**Config-change audit.** `audit.go` appends one record per accepted config change
(timestamp, the field-level diff, and the source — local file, flag, or a dashboard
`PUT`). This is the "history of what was changed" the dashboard surfaces.

Metering writes are append-only and never block the engine: samples go through a
buffered channel to a dedicated writer goroutine.

### 6.11 The engine loop

`internal/engine/engine.go` ties §6.1–6.10 together:

```
load config + state + geoip + targets + metering
on start: write a "service started" log line, recover any stale state

loop forever:
    if not general.running (Stopped via the dashboard): sleep short, continue

    raw      := netstat.Read(iface)
    detect reboot / update synchronizer            (6.2)
    deficit  := schedule.Deficit(mode, ...)         (6.3)
    metrics.SetDeficit(deficit); metering.Sample(raw, ...)   (6.10)

    if deficit > 0 and uploader enabled and (ASNs selected or dev mode on):
        workers := dev.workers in dev mode, else curve-derived & slew-limited (6.4)
        sent    := uploader.RunCycle(ctx, budget, workers)      (6.8)
        log a "cycle complete" line with workers + bytes + per-ASN
        raw = netstat.Read(iface); recompute tracked totals

    state.Save()                                    # atomic
    sleep randomDuration(cfg.IntervalMin, cfg.IntervalMax)
```

The cycle sleep is a random 5–30 s — there is **no hourly tick**; time-of-day shaping
is entirely the continuous curve's job (§6.4), so nothing in the loop is periodic on
the hour. Every iteration is wrapped in `recover()` so a panic logs a stack trace and
the loop continues — the daemon never dies from a transient fault (flaw #3).

The Start/Stop state lives in `general.running` and is persisted to `config.json`
every time the dashboard toggles it. After an unwanted restart — crash, reboot, or
systemd `Restart=on-failure` — the engine reads `config.json` on startup and resumes
in exactly the run state it was last left in.

---

## 7. Package-by-package specification

### 7.1 `cmd/tavazon/main.go`

Entrypoint, wiring only — no business logic:

- Parse CLI flags (§12); load config (`config.Load`); apply env + flag overrides;
  `config.Validate`.
- Initialise logging (`logging.Setup`).
- Load persistent state (`state.Load`).
- Open GeoIP databases (`geoip.Open`); build `targets.New` from the selected ASNs.
- Open the metering store (`metering.Open`).
- Construct `metrics.Registry`.
- Start the web server goroutine (`web.Server`) if `web.enabled`.
- Start the engine (`engine.Run`) in the main goroutine.
- Install `signal.NotifyContext` for `SIGINT`/`SIGTERM`; on signal cancel the context,
  let the engine finish its current cycle, flush metering, save state, flush logs,
  exit 0.

### 7.2 `internal/config`

```go
type Config struct {
    General  GeneralConfig
    Dev      DevConfig       // local-testing mode (§6.6)
    Target   TargetConfig    // mode + ratio + volume
    Network  NetworkConfig
    GeoIP    GeoIPConfig     // db paths + selected ASNs
    Uploader UploaderConfig
    Curve    CurveConfig     // anchors + wander params
    Targets  TargetsConfig   // port range + cache TTLs
    State    StateConfig
    Metering MeteringConfig
    Web      WebConfig
    Log      LogConfig
}
```

(Full field list in §8.) Functions: `Load`, `Default`, `Validate`, `ApplyEnv`,
`ApplyFlags`. `Validate` range-checks every coefficient, rejects unparseable durations,
ensures both `.mmdb` paths exist, and warns when `web.listen` is non-local with an empty
`auth_token`. Config is **reloadable at runtime** behind an `RWMutex`; a
`PUT /api/config` takes effect on the next cycle without a restart and is recorded by
the metering audit log.

### 7.3 `internal/netstat`

`/proc/net/dev` parser, §6.1. `Read(iface)`, `parse(r io.Reader, iface)`, and
`Interfaces() []string` for the dashboard's interface picker.

### 7.4 `internal/geoip`

MaxMind reader + ASN index, §6.5. Wraps `maxminddb-golang`. Two test paths: `Open` is
exercised against the operator's real `maxmind_files/` databases (`t.Skip` if absent);
`NewForTest` builds the index from in-memory maps so other packages test fully offline.
No `.mmdb` is committed.

### 7.5 `internal/state`

```go
type CacheEntry struct {
    Port    int       `json:"port"`
    ASN     uint32    `json:"asn"`
    Expires time.Time `json:"expires"`
}
type State struct {
    mu              sync.Mutex
    TotalUpload     int64                 `json:"total_upload"`
    TotalDownload   int64                 `json:"total_download"`
    UploadSync      int64                 `json:"upload_sync"`
    DownloadSync    int64                 `json:"download_sync"`
    WindowStart     time.Time             `json:"window_start"`      // volume mode
    WindowSentBytes int64                 `json:"window_sent_bytes"` // volume mode
    IPCache         map[string]CacheEntry `json:"ip_cache"`
    UpdatedAt       time.Time             `json:"updated_at"`
    path            string
}
```

`Load`, `Save` (atomic: marshal → `path+".tmp"` → `fsync` → `os.Rename`),
`PurgeExpired(now)`. Mutating accessors guarded by `mu`.

### 7.6 `internal/schedule`

Pure, no I/O. `schedule.go`: `Deficit(...)` for both target modes (§6.3).
`curve.go`: the periodic Catmull-Rom interpolation and the mean-reverting wander (§6.4).
The easiest package to unit-test exhaustively.

### 7.7 `internal/targets`

ASN-based target generation (§6.6). `New`, `RandomTarget`, weighted ASN/prefix picking.

### 7.8 `internal/uploader`

`uploader.go`: the `Uploader` type and `RunCycle(ctx, budget, workers) int64`.
`payload.go`: `RandomSize`, `Random`, and the token bucket. Pure-random payloads (§6.9).

### 7.9 `internal/metering`

The time-series store, per-ASN accounting, percentile calc, and config audit (§6.10).
`Open`, `Sample`, `RecordSend`, `Percentile`, `History(from,to,granularity)`,
`AppendAudit`. A dedicated writer goroutine drains a buffered channel so the engine
never blocks on disk I/O.

### 7.10 `internal/engine`

`Run(ctx, ...)` — the loop of §6.11. Owns no UI; only orchestrates. It exposes **no**
start/stop method: each cycle it reads `general.running` from the live (reload-aware)
config. The dashboard's Start/Stop and Dev-mode buttons are handled by the web layer,
which mutates the shared config, persists `config.json`, and audits — exactly as
`PUT /api/config` does — so the engine sees the change on the next cycle and the run
state survives a restart (§6.11).

### 7.11 `internal/metrics`

In-memory, concurrency-safe (atomic counters + `RWMutex` for sample buffers).
`AddFakeBytes`, `ObserveCounters`, `SetDeficit`, `SetWorkers`, `SetResources`,
`Snapshot`, `Prometheus`. `Snapshot` feeds `/api/stats` (§10.2), including the
CPU / RAM / bandwidth resource block.

### 7.11a `internal/sysstat`

Process resource sampler feeding the dashboard's resource panel (§10.3) and the
resource metrics (§11.1). `Read() (Sample, error)` parses `/proc/self/stat` (CPU
jiffies — `utime+stime`), `/proc/self/status` (`VmRSS`), and `/proc/meminfo`
(`MemTotal`). CPU% is derived by the caller from two `Sample`s, the elapsed wall
time, and `runtime.NumCPU()`. Linux-only, like `netstat`; a `parse` helper is split
out so tests feed fixtures. Stdlib only — no dependency.

### 7.12 `internal/logging`

`Setup(cfg LogConfig) (*slog.Logger, error)` — `slog` JSON logger to a **self-rotating**
file (rename to `tavazon.log.1`, keep `MaxBackups`, reopen) mirrored to stderr. Fixes
flaw #9.

### 7.13 `internal/web`

See §10.

---

## 8. Configuration schema

`config.json`. All durations are Go duration strings. Defaults shown.

```jsonc
{
  "general": {
    "interval_min": "5s",        // min sleep between engine cycles
    "interval_max": "30s",       // max sleep between engine cycles
    "running": true              // persisted Start/Stop state — restored after a restart
  },
  "dev": {                       // local-testing mode — see §6.6 and RED_LINES E1
    "enabled": false,            // dashboard toggle button; OFF in production
    "target": "192.168.1.1",     // explicit local IP all junk is sent to when enabled
    "workers": 4                 // worker count in dev mode (one IP — kept low)
  },
  "target": {
    "mode": "ratio",             // "ratio" | "volume"
    "ratio": {
      "multiplier": 8,           // upload = download x this (editable in dashboard)
      "jitter": 0.3,             // +/- fraction applied to multiplier each cycle
      "min_deficit_bytes": 1073741824   // 1 GiB; below this, skip the cycle
    },
    "volume": {
      "bytes": 1073741824,       // N — budget per window
      "window": "24h"            // M — window length; budget repeats each window
    }
  },
  "network": {
    "interface": "",             // "" = auto-sum all non-loopback up interfaces
    "link_capacity_mbit": 0      // NIC link speed for the bandwidth gauge; 0 = auto-detect from /sys (min 0)
  },
  "geoip": {
    "asn_db": "maxmind_files/GeoLite2-ASN.mmdb",
    "country_db": "maxmind_files/GeoLite2-Country.mmdb",
    "picker_country": "IR",      // dashboard ASN picker pre-filters to this country
    "selected_asns": []          // AS numbers to send to; empty => uploader idle
  },
  "uploader": {
    "threads_coefficient": 3,        // 1..30; base workers = this x 10
    "speed_coefficient": 1,          // 1..5; token-bucket rate multiplier
    "base_rate_bps": 524288,         // bytes/s that speed_coefficient=1 maps to (min 65536)
    "cycle_budget_fraction": 0.3,    // fraction of the deficit sent per cycle (min 0.05, max 1.0)
    "packet_gap_max": "10ms",        // upper bound of the random inter-packet sleep (min 0s)
    "max_workers": 300,              // hard cap on concurrent workers (min 1)
    "max_ramp": 20,                  // max worker-count change between cycles — slew (min 1)
    "min_datagram": 64,              // bytes; smallest random datagram (min 1)
    "max_datagram": 1472             // bytes; largest — un-fragmented (min > min_datagram, max 1472)
  },
  "curve": {                     // continuous 24h shape — see 6.4
    "anchors": [0.6,0.4,0.3,0.2,0.05,0.05,0.2,0.6,1.0,1.2,1.3,1.4,
                1.3,1.35,1.5,1.45,1.3,1.45,1.7,1.9,2.0,1.5,1.1,0.8],
    "max": 2.5,                  // hard ceiling on intensity after wander
    "wander_strength": 0.15,     // amplitude of the slow random walk
    "wander_reversion": "30m"    // mean-reversion time constant of the walk
  },
  "targets": {
    "port_min": 1024,
    "port_max": 65535,
    "cache_ttl_min": "10m",
    "cache_ttl_max": "100m"
  },
  "state": {
    "file": "data/state.json",
    "save_interval": "30s"
  },
  "metering": {
    "dir": "data/metering",
    "retention_5min": "9000h",   // ~13 months of 5-minute samples
    "billing_window": "month",   // "month" | a duration like "720h"
    "percentile": 95             // billing percentile
  },
  "web": {
    "enabled": true,
    "listen": "127.0.0.1:8080",
    "auth_token": ""             // if set, required as Bearer token / ?token=
  },
  "log": {
    "file": "data/tavazon.log",
    "level": "info",
    "max_size_mb": 10,
    "max_backups": 3
  }
}
```

**Env-var overrides** (for Docker): every scalar is reachable as
`TAVAZON_<SECTION>_<FIELD>`, e.g. `TAVAZON_TARGET_RATIO_MULTIPLIER=8`,
`TAVAZON_WEB_LISTEN=0.0.0.0:8080`. **Flag overrides** take final precedence (§12).

> **Security note on `web.listen`:** the dashboard can change config and reset
> counters. It binds `127.0.0.1` by default. Exposing it on `0.0.0.0` **must** be
> paired with a non-empty `auth_token` (and ideally a TLS-terminating reverse proxy).
> `Validate()` warns loudly if `listen` is non-local and `auth_token` is empty.

---

## 9. Persistent state & metering store

All runtime files live under `data/` and are **gitignored**.

### 9.1 `data/state.json`

The `State` struct in §7.5, written atomically. Holds the synchronizer offsets, lifetime
totals, the volume-mode window cursor, and the TTL IP cache. On first run the file does
not exist and numeric fields start at 0 — the first cycle anchors the synchronizer to
the current kernel counters.

### 9.2 `data/metering/` — the history store

A directory of append-only flat files (no database server):

```
data/metering/
├── live.bin                 # per-second ring (24h) snapshot, restored on startup
├── 2026-05.5min.jsonl       # one file per calendar month of 5-minute samples
├── daily.jsonl              # one line per day — long-term rollup, kept forever
└── audit.jsonl              # append-only config-change audit log
```

Each 5-minute sample line:

```json
{"t":"2026-05-17T20:30:00+03:30","up_bps":5242880,"down_bps":131072,
 "up_bytes":1572864000,"down_bytes":39321600,"fake_bytes":1310720000,
 "per_asn":{"58224":820000000,"44244":490720000}}
```

Each audit line records timestamp, source (`file` | `flag` | `api`), and a field-level
diff of the config change. Old `*.5min.jsonl` files past `retention_5min` are deleted by
a housekeeping pass; `daily.jsonl` is never trimmed (a line is ~120 bytes).

### 9.3 `maxmind_files/GeoLite2-ASN.mmdb`, `maxmind_files/GeoLite2-Country.mmdb`

Operator-supplied GeoLite2 databases, kept in `maxmind_files/` — separate from the
`data/` runtime directory. **Never committed** — the MaxMind EULA forbids
redistribution, and the files are large (tens of MB) and updated frequently. The
README explains how to obtain them. `.gitignore` excludes `*.mmdb` entirely, so no
`.mmdb` is ever committed — not even a test fixture (§15).

---

## 10. Web dashboard & HTTP API

`internal/web`. Pure stdlib `net/http`. The dashboard (`static/index.html`) is a single
self-contained file — inline CSS, inline vanilla JS, **no external CDN** (it must work
on a censored network) — embedded with `//go:embed`. There is **no Grafana**, no
external monitoring stack; this dashboard *is* the monitoring surface.

### 10.1 Endpoints

| Method | Path | Purpose |
|---|---|---|
| `GET`  | `/` | Serve the dashboard HTML |
| `GET`  | `/api/stats` | Live metrics snapshot (dashboard polls every 2 s) |
| `GET`  | `/api/config` | Current config as JSON |
| `PUT`  | `/api/config` | Replace config; validated, applied live, persisted, audited |
| `GET`  | `/api/asns?country=IR` | List ASNs from the GeoIP db (for the picker) |
| `GET`  | `/api/history?from=&to=&granularity=` | Time-series for the history charts |
| `GET`  | `/api/billing` | 95th-percentile rate + total volume for the billing window |
| `GET`  | `/api/audit?n=100` | Recent config-change audit records |
| `POST` | `/api/control/start` | Start traffic generation — sets `general.running` true, persists `config.json`, audited |
| `POST` | `/api/control/stop` | Stop traffic generation — sets `general.running` false, persists, audited |
| `POST` | `/api/control/dev` | Body `{ "enabled": bool }` — toggle dev mode; persists `config.json`, audited |
| `POST` | `/api/control/reset-counters` | Set tracked totals to 0 (re-anchor sync) |
| `POST` | `/api/control/set-sync` | Body `{ "upload_gb": N, "download_gb": M }` — manually set lifetime totals |
| `GET`  | `/api/logs?n=200` | Tail the last N log lines |
| `GET`  | `/metrics` | Prometheus text exposition (optional, for those who want it) |
| `GET`  | `/healthz` | Liveness probe → `200 ok` |

All `/api/*` and `/metrics` require the `auth_token` (header `Authorization: Bearer
<token>` or `?token=`) **iff** one is configured. `/healthz` is always open.

### 10.2 `/api/stats` response shape

```json
{
  "uptime_seconds": 8123,
  "running": true,
  "mode": "ratio",
  "dev": { "enabled": false, "target": "192.168.1.1" },
  "tracked": { "upload": 987654321000, "download": 123456789000 },
  "raw":     { "upload": 87654321000,  "download": 23456789000  },
  "sync":    { "upload": 900000000000, "download": 100000000000 },
  "speed":   { "upload_bps": 5242880, "download_bps": 131072 },
  "ratio":   { "current": 8.0, "target": 8.0 },
  "deficit_bytes": 4294967296,
  "workers_active": 42,
  "curve_intensity": 1.73,
  "resources": {
    "cpu_pct": 4.2, "ram_used_bytes": 18874368, "ram_total_bytes": 2097152000,
    "ram_pct": 0.9, "bandwidth_bps": 5373952, "link_capacity_bps": 125000000,
    "bandwidth_pct": 4.3
  },
  "fake_bytes_cycle": 314572800,
  "fake_bytes_total": 9876543210,
  "per_asn_cycle": { "58224": 200000000, "44244": 114572800 },
  "tehran_time": "2026-05-17T20:30:12+03:30",
  "speed_samples": [ /* per-second ring buffer for the 24h chart */ ]
}
```

### 10.3 Dashboard UI

A single dark-themed page. Sections:

- **Header** — project name, uptime, a single **Start/Stop** toggle button (the
  persisted run state), a **Dev-mode** toggle button, connection indicator, current
  target mode. While dev mode is on, a prominent banner reads
  `DEV MODE — all traffic → <dev.target>` so a test session is never mistaken for
  production.
- **Stat cards** — Σ Upload, Σ Download, current ratio (coloured green/yellow/red),
  ⇧ speed, ⇩ speed, active workers, current deficit, live curve intensity.
- **24-hour traffic chart** — a `<canvas>` line chart of bytes-per-second across the
  day, fed from the per-second ring buffer. Hand-drawn on the canvas; no chart library.
- **Resource panel** — three hand-drawn `<canvas>` pie/donut gauges for the Tavazon
  process: **CPU** (% of all cores), **RAM** (RSS as % of system memory), and
  **Bandwidth** (current up+down as % of `link_capacity`). Each is a pie of
  used-vs-free with the absolute figure printed in the centre, fed from `resources`
  in `/api/stats`. It lets the operator watch the cost of a high `speed_coefficient`.
- **Billing panel** — the current billing window's **95th-percentile rate** (⇧ and ⇩)
  and **total volume**, the two quantities the provider charges on, with the per-ASN
  split of the fake portion.
- **Per-ASN breakdown** — a table/bar view of how much traffic went to each selected
  ASN, over a selectable period.
- **History view** — charts over days/weeks/months from `/api/history` (volume, 95th
  percentile trend) so the operator can see what happened while away.
- **Settings panel** — a form bound to `/api/config`: target mode switch (ratio ↔
  volume), ratio multiplier, volume budget + window, and **every tunable constant** —
  `speed_coefficient`, `threads_coefficient`, `base_rate_bps`, `cycle_budget_fraction`,
  `packet_gap_max`, `max_workers`, `max_ramp`, `min_datagram`/`max_datagram`,
  `link_capacity_mbit`, `dev.workers` — each an input that enforces its `Validate`
  minimum; plus the interface picker, **ASN picker** (populated from
  `/api/asns?country=IR`, multi-select with search), curve anchors, and the
  `dev.target` local IP field. "Apply" issues `PUT /api/config`. (Start/Stop and
  Dev-mode are the header toggle buttons, not part of this form.)
- **Settings history** — the config-change audit log from `/api/audit`: what changed,
  when, and from where.
- **Counter tools** — "Reset counters" and "Set lifetime totals", each behind a
  confirmation dialog.
- **Log tail** — last ~200 lines from `/api/logs`, auto-refreshing.

The JS does nothing but `fetch()` the JSON endpoints on a timer and repaint — no
framework, no build step.

---

## 11. Metrics & logging

### 11.1 Prometheus `/metrics`

Optional, plain-text exposition built by hand (no client library → no dependency). For
operators who already run a Prometheus stack; the built-in dashboard does not need it.

```
tavazon_upload_bytes_total 987654321000
tavazon_download_bytes_total 123456789000
tavazon_fake_bytes_total 9876543210
tavazon_deficit_bytes 4294967296
tavazon_workers_active 42
tavazon_upload_bps 5242880
tavazon_curve_intensity 1.73
tavazon_p95_upload_bps 4194304
tavazon_cpu_percent 4.2
tavazon_ram_used_bytes 18874368
tavazon_bandwidth_bps 5373952
tavazon_running 1
tavazon_dev_mode 0
tavazon_uptime_seconds 8123
```

The `cpu_percent`, `ram_used_bytes`, and `bandwidth_bps` series come from
`internal/sysstat` (§7.11a) — the same data the dashboard's resource panel renders —
sampled once per engine cycle.

### 11.2 Logging

`slog` JSON records, one per line, to the rotating file (§7.12) and mirrored to stderr.
Events logged:

- `service started` / `service stopping`
- `geoip loaded` (ASN count, country counts) / `geoip missing` (clear remediation)
- `reboot detected` (old/new synchronizer values)
- `cycle complete` (bytes sent, duration, workers, per-ASN split)
- `config reloaded` (which fields changed — also written to the audit log)
- `error` events — every recovered panic and every non-fatal network/file error,
  **with context**, never silently swallowed.

Per-packet / per-worker detail is `debug`-level only. Byte sizes are human-formatted
(`12.34 GiB`) by a `humanize` helper.

---

## 12. CLI interface

```
tavazon [flags]

  -config string     path to config.json            (default "config.json")
  -state string      override state file path
  -asn-db string     override GeoLite2-ASN.mmdb path
  -country-db string override GeoLite2-Country.mmdb path
  -listen string     override web listen address
  -mode string       override target mode: ratio|volume
  -multiplier int    override ratio multiplier
  -stopped           start in the stopped state (overrides general.running)
  -no-web            disable the web dashboard
  -log-level string  debug|info|warn|error
  -print-config      print the effective merged config as JSON and exit
  -version           print version and exit
```

Precedence (lowest → highest): **built-in defaults → config.json → `TAVAZON_*` env
vars → CLI flags**.

---

## 13. Concurrency model

- **Main goroutine** — runs `engine.Run`; the loop of §6.11.
- **Web goroutine** — `http.Server`; started only if `web.enabled`.
- **Worker goroutines** — spawned per cycle by `uploader.RunCycle`, bounded by
  `max_workers`, all joined via a `sync.WaitGroup` before the cycle returns.
- **Metering writer goroutine** — drains a buffered channel of samples to the
  append-only files so the engine never blocks on disk I/O.
- **State save goroutine** — a `time.Ticker` every `state.save_interval`.

Shared data:

- `Config` — behind `sync.RWMutex`; engine reads, web `PUT` writes.
- `State` — behind its own `sync.Mutex`.
- `metrics.Registry` — `atomic` counters + `RWMutex` for sample buffers.
- `geoip.GeoIP` — read-only after `Open`, so lock-free.
- A single `context.Context` from `signal.NotifyContext` threads through everything;
  cancellation = graceful shutdown.

Every goroutine body is wrapped in `defer recover()` that logs the panic and (for the
engine) continues the loop.

---

## 14. Deployment artifacts

### 14.1 `Dockerfile`

```dockerfile
# ---- build ----
FROM golang:1.22 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -mod=vendor -ldflags "-s -w" -o /tavazon ./cmd/tavazon

# ---- run ----
FROM scratch
COPY --from=build /tavazon /tavazon
COPY config.example.json /config.json
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/tavazon", "-config", "/config.json"]
```

`-mod=vendor` builds from the committed `vendor/` tree — no network. The `.mmdb` files
are **not** baked in; they are mounted via the `/data` volume by the operator.

### 14.2 `docker-compose.yml`

```yaml
services:
  tavazon:
    build: .
    image: salehi/tavazon:latest
    restart: always
    network_mode: host          # needs the host's real NIC counters
    volumes:
      - ./data:/data                       # config, state, metering
      - ./maxmind_files:/maxmind_files:ro  # GeoLite2 .mmdb databases (read-only)
      - ./config.json:/config.json
    environment:
      - TAVAZON_TARGET_RATIO_MULTIPLIER=8
      - TAVAZON_WEB_LISTEN=127.0.0.1:8080
```

`network_mode: host` is required: the container must read the host's `/proc/net/dev`
and send from the host's real IP. No Redis container, no second service.

### 14.3 `systemd/tavazon.service`

```ini
[Unit]
Description=Tavazon - upload/download ratio balancer
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/tavazon
ExecStart=/opt/tavazon/tavazon -config /opt/tavazon/config.json
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/opt/tavazon/data
ProtectHome=true

[Install]
WantedBy=multi-user.target
```

Install = copy the binary + `config.json` into `/opt/tavazon/`, the two GeoLite2
`.mmdb` files into `/opt/tavazon/maxmind_files/`, drop the unit in `/etc/systemd/system/`,
`systemctl enable --now tavazon`.

---

## 15. Testing strategy

Unit tests for every pure-logic package — no real sockets, no real `/proc`. The one
exception is `geoip`, whose `Open` is tested against the operator's real GeoLite2
files (skipped when absent) because that is the only way to exercise the parser:

- `config_test.go` — defaults load; `Validate` rejects out-of-range values; env/flag
  overlay precedence.
- `netstat_test.go` — `parse()` against a checked-in `/proc/net/dev` fixture; loopback
  excluded; named-interface selection.
- `geoip_test.go` — `Open` against the operator's real `maxmind_files/` databases
  (`t.Skip` if absent), with property-style assertions: a known IP resolves to an ASN,
  `ListASNs("IR")` is non-empty and every entry is country `IR`, prefixes are non-empty.
  A separate case checks `NewForTest` builds a correct index from an in-memory map.
- `state_test.go` — round-trip save/load; atomic write leaves no partial file;
  `PurgeExpired`.
- `schedule_test.go` — ratio & volume `Deficit` sign and cutoffs; the curve is
  **C1-continuous and periodic** (no step at any hour boundary, value at 24⁻ equals
  value at 0); the curve reaches ~0 at the trough; wander stays bounded.
- `targets_test.go` — ASN-weighted selection covers every selected ASN; generated IPs
  fall inside the ASN's prefixes; ports inside `[port_min,port_max]`.
- `uploader_test.go` — datagram sizes are uniform in `[min,max]` and never exceed
  `max_datagram`; payload bytes are non-constant; token bucket delivers ≈ the
  configured rate over a window.
- `metering_test.go` — 5-minute bucketing; 95th-percentile calc against a known sample
  set; per-ASN totals; rollup correctness; audit append.

Plus one integration test: spin a local UDP listener on `127.0.0.1`, point a one-ASN
`geoip.NewForTest` graph (its single prefix `127.0.0.0/8`) at it, run a single short
`RunCycle`, assert bytes were received,
`metrics.fake_bytes_total` advanced, and metering recorded the ASN.

Target: every package compiles, `go vet` clean, `go test -race ./...` green.

---

## 16. Build

**No Go toolchain on the host, no `Makefile`, no CI.** Go is never installed locally —
every interaction with the toolchain happens inside a throwaway container with the
working tree bind-mounted. The repository carries no `.github/` workflows and no
`Makefile`. The only thing a developer installs is Docker.

### 16.1 The toolchain container

All dev commands follow one pattern — a `golang` container with the repo mounted at
`/work`:

```sh
docker run --rm -u "$(id -u):$(id -g)" -e GOCACHE=/tmp/gocache \
  -v "$PWD":/work -w /work golang:1.22 <go command>
```

Canonical `<go command>` values:

| Task   | `<go command>` |
|--------|----------------|
| build  | `sh -c 'CGO_ENABLED=0 go build -mod=vendor -trimpath -o tavazon ./cmd/tavazon'` |
| test   | `go test -race -mod=vendor ./...` |
| vet    | `go vet -mod=vendor ./...` |
| fmt    | `gofmt -l .` |
| vendor | `go mod vendor` |
| cross  | `sh -c 'CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -mod=vendor -trimpath -o tavazon-arm64 ./cmd/tavazon'` |

Notes:

- `go test -race` needs a C toolchain, so the Debian-based **`golang:1.22`** image is
  used — *not* `-alpine`, which ships no `gcc`.
- `-u "$(id -u):$(id -g)"` keeps files the container writes (`vendor/`, the binary)
  owned by the host user, not root.
- `-e GOCACHE=/tmp/gocache` gives the non-root user a writable build cache.

### 16.2 The application image

`Dockerfile` (§14.1) is multi-stage: its **build stage *is* the build environment** —
it runs `go build` inside a `golang` container exactly as §16.1 does, so producing the
shippable image needs nothing on the host but Docker. `docker-compose.yml` (§14.2)
builds and runs the result with `network_mode: host` so it sees the host's real
interfaces and RX/TX counters.

### 16.3 Dependencies

`go.mod`: `module namizungo`, `go 1.22`, a **single `require`** —
`github.com/oschwald/maxminddb-golang` — vendored into `vendor/`. All builds use
`-mod=vendor` so the build container needs **no network**. Regenerate `vendor/` with
the `vendor` command above after any `go.mod` change. The dependency allowlist is
closed; see [RED_LINES.md](RED_LINES.md) X1.

---

## 17. Implementation order

Build bottom-up so each layer is testable before the next depends on it. Phases and
their definition-of-done are detailed in [ROADMAP.md](ROADMAP.md).

1. `go.mod`, `vendor/` (maxminddb), repo skeleton, `.gitignore`, `LICENSE`,
   `Dockerfile`, `docker-compose.yml` — verify a build runs in the toolchain container.
2. `internal/config` + tests.
3. `internal/netstat` + fixture test.
4. `internal/geoip` (`Open` + the `NewForTest` seam) + tests.
5. `internal/state` + tests.
6. `internal/schedule` (ratio + volume + continuous curve) + tests.
7. `internal/targets` (ASN-based generation) + tests.
8. `internal/uploader` (`payload.go`, token bucket, `RunCycle`) + tests.
9. `internal/metering` (time-series, percentile, per-ASN, audit) + tests.
10. `internal/metrics`, `internal/logging`.
11. `internal/engine` — wire 2–10 together.
12. `internal/web` + `static/index.html` (full dashboard).
13. `cmd/tavazon/main.go` — final wiring, flags, signals.
14. `Dockerfile`, `docker-compose.yml`, `systemd/tavazon.service`,
    `README.md`, `config.example.json`.
15. End-to-end smoke test against a local UDP sink.

---

## 18. Legal & ethical note

Tavazon, like namizun, exists so operators of anti-censorship infrastructure in Iran
can stay within a quota policy that is itself a tool of network control. By design it
is **not** an attack tool:

- Per-destination volume is small and the destination set is large and constantly
  churned — no single host receives meaningful load.
- There is a hard `max_workers` cap and a token-bucket rate limiter; total egress is
  bounded and configurable.
- It targets only **AS numbers the operator deliberately selects** — intended to be the
  operator's own provider/national backbone ASNs, traffic that, by the provider's own
  metering, is "domestic" and discarded.

It should be run only on servers the operator controls, against a quota the operator is
legitimately subject to. It must not be repurposed to concentrate traffic on a single
target — doing so would turn a quota-balancer into a DoS tool, which this design
explicitly prevents (distributed targeting across many prefixes, per-IP caps, rate
limiting). See [RED_LINES.md](RED_LINES.md) for the inviolable boundaries.

---

*End of design document. Build in the order of §17 / [ROADMAP.md](ROADMAP.md); every
section above is a complete specification for the corresponding artifact.*
