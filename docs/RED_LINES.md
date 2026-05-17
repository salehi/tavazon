# Tavazon — Red Lines

> Hard constraints. A change that crosses any line below **fails review automatically**,
> regardless of how well it is written or how useful it seems. These are not
> preferences — they are the boundary of what this project is.
>
> Read alongside [project.md](project.md) §2 (why rewrite), §3 (goals & non-goals),
> and §18 (legal & ethical note). This document turns those sections into enforceable
> rules.

---

## 1. Ethical red lines

Tavazon exists to *balance* a quota, not to attack anything. The moment it can be
pointed at a victim, it stops being Tavazon. The following are inviolable.

### E1 — No traffic concentration

Generated traffic **must** be spread across many destination IPs. No code path may
exist that sends a cycle's full budget — or a disproportionate share of it — to a
single IP, host, prefix, or port.

- Target selection is always randomized: across selected ASNs, across each ASN's
  prefixes, and across hosts within a prefix (project.md §6.6).
- No config option, flag, env var, or API field may narrow the target set to a single
  address, or to a range small enough to behave like one.
- Selecting a single ASN is allowed (the operator's own provider) — but within it,
  distribution across prefixes and hosts is still mandatory.
- The IP cache (project.md §6.7) churns by TTL; it must never converge on a fixed set.

**Dev-mode exception.** `dev` mode (project.md §6.6) is a local-testing aid that
*deliberately* sends all traffic to one operator-configured IP, so the operator can
watch the whole pipeline in the dashboard without a VPS. It is the **single** sanctioned
exception to E1 and E2 — bounded by behaviour instead of by distribution:

- off by default; never a production configuration;
- loudly flagged the entire time it is active — a dashboard banner **and** a log line;
- still bound by the token-bucket rate limit and the `max_workers` cap (E3);
- the configured `dev.target` is **not** restricted to private ranges (an explicit
  project-owner decision). The operator is trusted to point it only at a host they
  own. Aiming it at a third party is exactly the misuse E1 exists to prevent — and is
  solely the operator's responsibility.

### E2 — Per-IP volume stays small

Per-destination volume must remain a trickle. A reviewer must be able to look at a
cycle and confirm no single IP receives meaningful load.

- Worker count scales the *number of target IPs*, never the load per IP.
- Datagram size is bounded by `max_datagram` (project.md §6.9); it is never a license
  to flood one target.

### E3 — Egress is always bounded

The token-bucket rate limiter (project.md §6.8) and the `max_workers` cap
(default 300) are **mandatory** and **always active**.

- No "unlimited" / "turbo" / disable-limiter mode may be added.
- `max_workers` is a hard ceiling applied *after* the curve and the slew limiter; the
  curve may never override it upward.
- `speed_coefficient` has a fixed maximum (5). It may not be uncapped.
- Volume mode's budget is a ceiling per window, not a floor to burst past.

### E4 — Targets stay inside deliberately-selected ASNs

Destinations come only from the AS numbers the operator explicitly selects, resolved
through the MaxMind GeoIP database. These are intended to be the operator's own
provider / national backbone ASNs — traffic the provider's metering treats as domestic.

- The dashboard ASN picker defaults to Iranian ASNs (`picker_country = "IR"`); other
  ASNs remain selectable, but selection is always a deliberate operator choice.
- No feature may accept arbitrary user-supplied destination IPs outside the prefixes
  of a selected ASN.
- With no ASN selected, the uploader stays idle — it never falls back to a default or
  guessed target set.

### E5 — Payloads are inert junk, never weapons

Datagram contents are fully random bytes (project.md §6.9), meant to be dropped at the
destination. They must never be crafted to exploit, probe, amplify, or trigger a
service.

- No protocol-specific payloads designed to elicit a response (no amplification
  vectors, no reflection, no exploit strings).
- We deliberately do *not* mimic any real protocol. Randomness is the strategy; a
  fabricated protocol header is forbidden — it would be both matchable and risk
  forming a valid request.

### E6 — Operator-controlled hosts only

Tavazon is for a server the operator controls, against a quota the operator is
legitimately subject to. This is a usage rule we cannot enforce in code, but:

- Documentation (README, dashboard, logs) must state this plainly.
- No feature may make it easier to run Tavazon covertly on a host that is not the
  operator's own.

### E7 — When in doubt, it is a non-goal

If a proposed feature's primary effect is to make traffic *less* distributed, *more*
concentrated, *higher* per-IP, or *harder to bound* — it is out of scope. Tavazon is
not a load tester, not a stress tool, not a DoS tool (project.md §3, non-goals).

---

## 2. Engineering red lines

These keep the binary self-contained, portable, and survivable — the whole point of
the rewrite (project.md §2, §4).

### X1 — Curated, vendored dependencies

Dependencies are an allowlist, not open season.

- **Permitted, and only this:** `github.com/oschwald/maxminddb-golang` and its
  transitive closure. Nothing else.
- Adding any other third-party module is an automatic rejection — including for
  logging, config parsing, charts, Prometheus, routing, or testing. Everything outside
  the allowlist is stdlib: `encoding/json`, `log/slog`, `embed`, `math/rand/v2`,
  `net/http`.
- All dependencies are **vendored** (`vendor/` is committed). Every build uses
  `-mod=vendor` and must succeed **fully offline** — the censored-network guarantee.
- Expanding the allowlist requires a [project.md](project.md) change first, in review.

### X2 — No CGO, fully static binary

- `CGO_ENABLED=0` for every build. `maxminddb-golang` is pure Go, so this holds.
- No `import "C"`. The binary must be a single static file that runs on a bare host.

### X3 — Application, not a library

- All packages live under `internal/`. Nothing is importable by third parties.
- `cmd/tavazon/main.go` contains wiring only — no business logic.

### X4 — No goroutine may crash the process

Every goroutine body — engine loop, web server, each worker, the metering writer, the
state-save ticker — is wrapped in `defer recover()` that logs the panic with context
(project.md §13).

- The engine loop recovers and **continues**; one bad cycle never kills the daemon.
- A panic that escapes any goroutine is a defect, not an accident.

### X5 — No silently swallowed errors

Every error is either handled meaningfully or logged with context (project.md §11.2).

- No bare `_ = err`, no empty `catch`-style blocks.
- Network/file errors inside a worker are logged and the loop continues — never
  ignored, never fatal to the cycle.

### X6 — No mandatory external service

No Redis, no database server, no Grafana, no monitoring stack is required to run.
State and history persist to local files (project.md §9). The dashboard's assets are
`embed`-bundled — **no CDN**, since the tool must work on a censored network.

### X7 — No hardcoded absolute paths

All paths come from config or flags (project.md §2, flaw #11). No `/var/www/...`,
no `/opt/tavazon/...` baked into Go source. The systemd unit may reference a path;
the binary may not.

### X8 — Atomic, corruption-proof state

State is written marshal → temp file → `fsync` → `os.Rename` (project.md §7.5). A
crash mid-write must never leave a corrupt or partial live state file. Metering files
are append-only so a torn write costs at most the last line.

### X9 — Concurrency is explicit and bounded

- No module-global mutable state shared across goroutines (project.md §2, flaw #12).
- Shared data is guarded: `Config` behind `RWMutex`, `State` behind `Mutex`, metrics
  via `atomic` + `RWMutex`; `geoip.GeoIP` is read-only after `Open` (project.md §13).
- Worker count is always known and capped; no unbounded goroutine fan-out.

### X10 — Linux runtime, cross-platform build

The runtime is Linux-only (it reads `/proc/net/dev`). The code must still **compile**
for other GOOS/GOARCH; only the netstat reader is platform-specific and isolated.

### X11 — No recursion for control flow

Loops and retries use iteration (project.md §2, flaw #4). No function may recurse as a
substitute for a loop.

### X12 — `.mmdb` files are never committed

The MaxMind GeoLite2 EULA forbids redistribution; the files are also large and updated
frequently. They are operator-supplied at runtime, kept in `maxmind_files/`.

- `.gitignore` excludes `*.mmdb`. A commit that adds one is rejected.
- **No** `.mmdb` is committed — not even a test fixture. `geoip` tests run against the
  operator's real files (skipped when absent); every other package uses the in-memory
  `geoip.NewForTest` constructor (project.md §6.5).

---

## 3. How these are enforced

There is **no CI** (project.md §16). Enforcement is human and local:

- **Containerised quality gates** — `go vet` and `go test -race`, run in the toolchain
  container (project.md §16), must pass before any push; the build must succeed
  offline (`-mod=vendor`). There is no `Makefile` and no Go on the host. See
  [CONTRIBUTING.md](CONTRIBUTING.md).
- **Code review** checks every change against this list and against the 15+3-flaws
  acceptance criteria in [STANDARDS.md](STANDARDS.md).
- **Tests** assert the mechanically-checkable bounds: worker count respects
  `max_workers`, target selection is distributed across ASNs/prefixes, `RandomSize`
  never exceeds `max_datagram`, the token bucket holds the configured rate.
- **A pre-commit grep** rejects a non-allowlisted import path or a committed `.mmdb`
  larger than the test fixture.

A red line has no exception process. If a real need conflicts with one, the design
doc changes first, in review, before any code does.
