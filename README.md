# Tavazon

Tavazon (تـوازن — "balance") keeps an asymmetric hosting quota healthy. Many
backbone providers bill on the *ratio* of upload to download, or on a 95th-
percentile rate; a server that mostly *downloads* drifts out of balance.
Tavazon measures the host's real NIC counters and manufactures just enough
junk UDP traffic — shaped like an organic day — to keep the ratio where the
provider wants it.

It is a single static Go binary with an embedded dashboard. No database, no
Redis, no external monitoring stack.

## Ethical boundary

Tavazon generates traffic **to and from infrastructure you operate or are
authorised to balance**. It is not a stress-testing or flooding tool. The hard
limits — what it must never be pointed at, and why — are spelled out in
[docs/RED_LINES.md](docs/RED_LINES.md). Read them before deploying.

## Obtaining the GeoLite2 databases

Tavazon resolves its destination IPs from MaxMind's free GeoLite2 databases.
They are **not** included — MaxMind's licence forbids redistribution, and
`.gitignore` excludes every `*.mmdb` so none is ever committed.

1. Create a free account at <https://www.maxmind.com/en/geolite2/signup>.
2. Download **GeoLite2 ASN** and **GeoLite2 Country** (the `.mmdb` format).
3. Place both files in `maxmind_files/`:

   ```
   maxmind_files/GeoLite2-ASN.mmdb
   maxmind_files/GeoLite2-Country.mmdb
   ```

If the databases are missing Tavazon still starts — the dashboard comes up and
shows the problem — but the uploader stays idle until they are provided.

## Running with systemd

```sh
sudo mkdir -p /opt/tavazon/data /opt/tavazon/maxmind_files
# download a release binary (or build one — see "Building" below) and copy it in:
sudo cp tavazon /opt/tavazon/tavazon
sudo cp config.example.json /opt/tavazon/config.json
sudo cp maxmind_files/*.mmdb /opt/tavazon/maxmind_files/
sudo cp systemd/tavazon.service /etc/systemd/system/
sudo systemctl enable --now tavazon
```

The unit ([systemd/tavazon.service](systemd/tavazon.service)) runs hardened —
`NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, with only
`/opt/tavazon/data` writable — and restarts on failure.

## The dashboard

Tavazon serves a self-contained dashboard (no CDN, works on a censored
network) — live stats, the 24-hour traffic curve, CPU/RAM/bandwidth gauges,
billing, per-ASN breakdown, history, and a settings form with a live ASN
picker. Start/Stop and Dev-mode are header toggle buttons.

By default it binds **`127.0.0.1:8081`** — local only. If you change
`web.listen` to a non-local address you **must** set `web.auth_token`; the
dashboard can rewrite config and reset counters, so an unprotected non-local
bind is dangerous. Tavazon logs a loud warning if you do this without a token.
Put a TLS-terminating reverse proxy in front of any remote exposure.

## CLI

```
tavazon [flags]
  -config string      path to config.json (default "config.json")
  -state string       override state file path
  -asn-db string      override GeoLite2-ASN.mmdb path
  -country-db string  override GeoLite2-Country.mmdb path
  -listen string      override web listen address
  -mode string        override target mode: ratio|volume
  -multiplier int     override ratio multiplier
  -stopped            start in the stopped state
  -no-web             disable the web dashboard
  -log-level string   debug|info|warn|error
  -print-config       print the effective merged config and exit
  -version            print version and exit
```

Settings precedence, lowest to highest: **built-in defaults → `config.json` →
`TAVAZON_*` environment variables → CLI flags**.

## Building

Prebuilt binaries for **linux, macOS and Windows** across **x86_64, arm64 and
armv7** are published on the [Releases](https://github.com/salehi/namizungo/releases)
page. They are built by GitHub Actions
([.github/workflows/release.yml](.github/workflows/release.yml)) on every tag
that looks like `release-1.0.0`.

To build locally you need Go 1.22+:

```sh
go test -race -mod=vendor ./...                       # full suite
go test -race -mod=vendor -tags e2e ./...             # + e2e smoke test
CGO_ENABLED=0 go build -mod=vendor -trimpath \
  -ldflags "-s -w" -o tavazon ./cmd/tavazon           # release binary
```

Cross-compile by setting `GOOS`/`GOARCH` (e.g. `GOOS=linux GOARCH=arm64`).
Builds are offline: dependencies are vendored under `vendor/`. The full
contributor workflow is in [docs/CONTRIBUTING.md](docs/CONTRIBUTING.md); the
design rationale is in [docs/project.md](docs/project.md).

## Licence

See [LICENSE](LICENSE).
