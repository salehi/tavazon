# Phase 1 — Data & I/O packages

> Plan for [ROADMAP.md](../ROADMAP.md) Phase 1. The packages that read the outside
> world: configuration, kernel counters, GeoIP, and persistent state.

**Delivers:** `internal/config`, `internal/netstat`, `internal/geoip`, `internal/state`
— all unit-tested in isolation.
**Depends on:** Phase 0.
**Unblocks:** Phase 2 (schedule/targets need config + geoip), Phase 4 (engine).

Build the tasks in order: `config` → `netstat` → `geoip` → `state`.

---

## 1.1 `internal/config`

Implements [project.md §7.2, §8](../project.md).

**Types** — one struct per config section, JSON tags `snake_case` exactly matching
project.md §8:

- `Config` with fields `General, Dev, Target, Network, GeoIP, Uploader, Curve,
  Targets, State, Metering, Web, Log`.
- `GeneralConfig{IntervalMin, IntervalMax time.Duration; Running bool}` — `Running`
  is the persisted Start/Stop run state (project.md §6.11); durations unmarshalled
  from Go duration strings (custom `UnmarshalJSON`, or `string` fields parsed in
  `Validate`).
- `DevConfig{Enabled bool; Target string; Workers int}` — local-testing mode
  (project.md §6.6); `Target` is the explicit local destination IP.
- `TargetConfig{Mode string; Ratio RatioConfig; Volume VolumeConfig}`.
- `RatioConfig{Multiplier float64; Jitter float64; MinDeficitBytes int64}`.
- `VolumeConfig{Bytes int64; Window time.Duration}`.
- `NetworkConfig{Interface string; LinkCapacityMbit int}`.
- `GeoIPConfig{ASNDB, CountryDB, PickerCountry string; SelectedASNs []uint32}`.
- `UploaderConfig{ThreadsCoefficient, SpeedCoefficient, MaxWorkers, MaxRamp,
  MinDatagram, MaxDatagram int; BaseRateBPS int64; CycleBudgetFraction float64;
  PacketGapMax time.Duration}`.
- `CurveConfig{Anchors [24]float64; Max float64; WanderStrength float64;
  WanderReversion time.Duration}`.
- `TargetsConfig{PortMin, PortMax int; CacheTTLMin, CacheTTLMax time.Duration}`.
- `StateConfig{File string; SaveInterval time.Duration}`.
- `MeteringConfig{Dir string; Retention5Min time.Duration; BillingWindow string;
  Percentile int}`.
- `WebConfig{Enabled bool; Listen, AuthToken string}`.
- `LogConfig{File, Level string; MaxSizeMB, MaxBackups int}`.

**Functions:**

- `Default() *Config` — every field set to the project.md §8 default.
- `Load(path string) (*Config, error)` — start from `Default()`, overlay the JSON
  file if present; a missing file returns the default with a logged warning.
- `(*Config) ApplyEnv()` — overlay `TAVAZON_<SECTION>_<FIELD>` env vars.
- `(*Config) ApplyFlags(fs *flag.FlagSet)` — overlay CLI flags (Phase 5 passes the
  set; here just the mechanism).
- `(*Config) Validate() error` — range-check every coefficient
  (`Multiplier ∈ [2,15]`, `SpeedCoefficient ∈ [1,5]`, `ThreadsCoefficient ∈ [1,30]`,
  `MinDatagram < MaxDatagram ≤ 1472`, `PortMin < PortMax`, percentile ∈ [50,99], …);
  reject `Mode` not in `{ratio,volume}`; confirm both `.mmdb` paths exist; **warn**
  (not fail) when `Web.Listen` is non-local and `AuthToken` is empty. When
  `Dev.Enabled`, `Dev.Target` must parse as an IP and `Dev.Workers ≥ 1` — the dev
  target is **not** range-restricted (RED_LINES E1 dev-mode exception). Each tunable
  constant has an enforced minimum: `BaseRateBPS ≥ 65536`,
  `CycleBudgetFraction ∈ [0.05,1.0]`, `PacketGapMax ≥ 0`, `MaxWorkers ≥ 1`,
  `MaxRamp ≥ 1`, `MinDatagram ≥ 1`, `MaxDatagram ∈ (MinDatagram,1472]`,
  `LinkCapacityMbit ≥ 0`.

**Precedence:** defaults → file → env → flags. Keep the overlay steps separate so the
order is explicit and testable.

**`config_test.go`:**

- `Default()` round-trips through JSON unchanged.
- `Validate` rejects each out-of-range coefficient (table-driven).
- env overlay: set `TAVAZON_TARGET_RATIO_MULTIPLIER=10`, assert it wins over the file.
- flag overlay beats env beats file beats default (one ordering test).
- missing `.mmdb` path → `Validate` error.

---

## 1.2 `internal/netstat`

Implements [project.md §6.1, §7.3](../project.md).

**Types & functions:**

- `Counters{TxBytes, RxBytes uint64}`.
- `Read(iface string) (Counters, error)` — opens `/proc/net/dev`, delegates to `parse`.
- `parse(r io.Reader, iface string) (Counters, error)` — split out for testing; sums
  the selected interfaces, **always excludes `lo`** when `iface == ""`.
- `Interfaces() ([]string, error)` — interface names for the dashboard picker.

**Parsing notes:** skip the two header lines; split each remaining line at `:`;
field 0 of the RX group = rx bytes, field 0 of the TX group = tx bytes (8 fields
each, project.md §6.1). "Up" detection for auto-sum: an interface with any nonzero
counter, or cross-check `/sys/class/net/<if>/operstate` — keep it simple, exclude
only `lo` for v1.

**`netstat_test.go`:**

- `testdata/proc_net_dev` fixture with `lo`, `eth0`, `eth1`.
- `parse("")` sums `eth0+eth1`, excludes `lo`.
- `parse("eth0")` returns only `eth0`.
- malformed line → error, not panic.

---

## 1.3 `internal/geoip`

Implements [project.md §6.5, §7.4](../project.md). Wraps `maxminddb-golang` (v1). No
`mmdbwriter`, no fixture-generation tool — that idea is dropped in favour of
`NewForTest`.

**Types & functions:**

- `GeoIP` — holds the opened readers (when built via `Open`) + the immutable ASN index.
- `ASNInfo{Number uint32; Name, Country string; Prefixes []net.IPNet; NumIPs uint64}`.
- `Open(asnPath, countryPath string) (*GeoIP, error)` — opens both `.mmdb` from disk;
  on a missing file returns a descriptive error (the caller keeps the dashboard up,
  project.md §6.5). Default paths: `maxmind_files/GeoLite2-{ASN,Country}.mmdb`.
- `NewForTest(prefixes map[uint32][]net.IPNet, country map[uint32]string) *GeoIP` —
  builds the **same in-memory index** with no file I/O. This is the test seam used by
  `targets`, `engine`, and the e2e test, so they need no `.mmdb` on disk.
- `(*GeoIP) ListASNs(country string) []ASNInfo` — `""` = all, `"IR"` = Iran only.
- `(*GeoIP) Prefixes(asn uint32) []net.IPNet`.
- `(*GeoIP) LookupASN(ip net.IP) (uint32, bool)`.

**ASN index build (shared):** factor a private `buildIndex(prefixes, country)` that
produces the immutable lookup structures. `Open` populates the input maps by walking
the ASN db with `reader.Networks()` — decode `{autonomous_system_number,
autonomous_system_organization}` per network, accumulate `NumIPs`, then tag each
ASN's country by sampling its prefixes against the Country db. `NewForTest` passes
its maps straight to `buildIndex`. Index is immutable after construction → no locking
(project.md §13).

**No committed fixture (RED_LINES X12).** No `.mmdb` is committed — not even a test
fixture. Two strategies replace it:

- `geoip`'s own test reads the operator's **real** files from `maxmind_files/` (path
  overridable via `TAVAZON_TEST_ASN_DB` / `TAVAZON_TEST_COUNTRY_DB`) and `t.Skip`s
  when absent — the only way to exercise the real parser.
- every other package's test uses `NewForTest` with hand-written prefixes.

**`geoip_test.go`** — property-style, since it runs against live data:

- `Open` against the real files succeeds; a bogus path → clear error.
- `LookupASN` of an IP inside a known large IR prefix returns a plausible ASN.
- `ListASNs("IR")` is non-empty and **every** entry has `Country == "IR"`;
  `ListASNs("")` is a superset of `ListASNs("IR")`.
- a separate, file-free case: `NewForTest` with 3 synthetic ASNs (2 `IR`, 1 `US`) →
  `LookupASN` / `Prefixes` / `ListASNs` return exactly the seeded data.
- the file `t.Skip`s cleanly when `maxmind_files/` is empty.

---

## 1.4 `internal/state`

Implements [project.md §6.7, §7.5, §9.1](../project.md).

**Types:**

- `CacheEntry{Port int; ASN uint32; Expires time.Time}` (JSON tagged).
- `State` — fields per project.md §7.5: `TotalUpload, TotalDownload, UploadSync,
  DownloadSync int64`, `WindowStart time.Time`, `WindowSentBytes int64`,
  `IPCache map[string]CacheEntry`, `UpdatedAt time.Time`; unexported `mu sync.Mutex`
  and `path string`.

**Functions:**

- `Load(path string) (*State, error)` — read JSON; missing file → zero-valued state
  with `path` set, `IPCache` initialised.
- `(*State) Save() error` — **atomic**: marshal → write `path+".tmp"` → `fsync` →
  `os.Rename` over `path`.
- `(*State) PurgeExpired(now time.Time)` — drop stale `IPCache` entries.
- Guarded accessors for the synchronizer fields and the cache (engine + web both
  touch `State`); all take `mu`.

**`state_test.go`:**

- save → load round-trip preserves every field.
- atomic write: simulate by checking no `*.tmp` remains and the file is valid JSON
  after `Save`; a marshal error must leave the previous file intact.
- `PurgeExpired` removes only entries with `Expires` before `now`.

---

## Definition of done

- [ ] `go test -race ./internal/{config,netstat,geoip,state}` green.
- [ ] Config precedence defaults→file→env→flags is test-verified.
- [ ] `geoip.Open` works against the real `maxmind_files/` databases (test skips if
      absent); `NewForTest` covers offline tests; no `.mmdb` is committed.
- [ ] Atomic state write leaves no partial file.
- [ ] Flaws #2, #6, #7-data, #10, #16 (STANDARDS.md §12) addressed.
- [ ] `gofmt`/`vet` clean; build offline.
