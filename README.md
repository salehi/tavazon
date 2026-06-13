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

## Install

One line, on Linux or macOS:

```sh
curl -fsSL https://raw.githubusercontent.com/salehi/tavazon/main/install.sh | sh
```

It downloads the release binary matching your OS and CPU (x86_64 / arm64 /
armv7) and lays out a self-contained directory at **`/opt/tavazon`**:

```
/opt/tavazon/
  tavazon              # the binary
  config.json          # copied from config.example.json (an existing one is kept)
  maxmind_files/       # you drop the GeoLite2 .mmdb files here
  data/                # runtime state, metering, logs
```

The script installs **this project only** — it does *not* fetch the GeoLite2
databases (MaxMind's licence forbids redistributing them). If the `.mmdb` files
are missing it prints a clear warning; see
[Obtaining the GeoLite2 databases](#obtaining-the-geolite2-databases) below.

Overrides:

```sh
curl -fsSL .../install.sh | TAVAZON_DIR=~/tavazon sh    # install somewhere writable (no sudo)
curl -fsSL .../install.sh | sh -s -- 1.0.0              # pin a version instead of the latest
```

Prefer to install by hand, or on **Windows**? Every release also ships archives
for all targets on the
[Releases](https://github.com/salehi/tavazon/releases) page.

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

The installer above already populates `/opt/tavazon` — the same path the unit
expects. After dropping the `.mmdb` files into `/opt/tavazon/maxmind_files/`:

```sh
curl -fsSL https://raw.githubusercontent.com/salehi/tavazon/main/systemd/tavazon.service \
  | sudo tee /etc/systemd/system/tavazon.service >/dev/null
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

## Licence

See [LICENSE](LICENSE).
