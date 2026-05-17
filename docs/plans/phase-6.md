# Phase 6 — Deployment & smoke test

> Plan for [ROADMAP.md](../ROADMAP.md) Phase 6. Make Tavazon shippable and prove it
> end to end.

**Delivers:** finalised Docker/systemd artifacts, `README.md`,
`config.example.json`, an end-to-end smoke test.
**Depends on:** Phase 1–5 (a complete, runnable binary).
**Unblocks:** release.

---

## 6.1 Finalise `Dockerfile`

The Phase 0 `Dockerfile` was a skeleton; confirm against the real build:

- build stage: `CGO_ENABLED=0 go build -trimpath -mod=vendor -ldflags "-s -w
  -X main.version=$(version)" -o /tavazon ./cmd/tavazon`.
- final stage `gcr.io/distroless/static-debian12`; copy the binary and
  `config.example.json` → `/config.json`. **Do not** copy any `.mmdb`.
- `VOLUME ["/data"]`, `EXPOSE 8080`, `ENTRYPOINT ["/tavazon","-config","/config.json"]`.

## 6.2 Finalise `docker-compose.yml`

Confirm: `build: .`, `network_mode: host`, volumes `./data:/data` (runtime state +
metering), `./maxmind_files:/maxmind_files:ro` (the GeoLite2 databases) and
`./config.json:/config.json`, the `TAVAZON_*` env examples, `restart: always`.

## 6.3 `systemd/tavazon.service`

Copy from [project.md §14.3](../project.md): `Type=simple`,
`WorkingDirectory=/opt/tavazon`, `Restart=on-failure`, the hardening directives
(`NoNewPrivileges`, `ProtectSystem=strict`, `ReadWritePaths=/opt/tavazon/data`,
`ProtectHome`).

## 6.4 `config.example.json`

The full project.md §8 schema with the documented defaults — `selected_asns` empty,
`auth_token` empty, paths under `data/`. This is the file the Docker image ships as
`/config.json` and the README points operators at.

## 6.5 `README.md`

User-facing. Must cover:

- what Tavazon is and the ethical boundary (link [RED_LINES.md](../RED_LINES.md)).
- **Obtaining the GeoLite2 `.mmdb` files** — create a MaxMind account, download
  GeoLite2-ASN + GeoLite2-Country, place them in `maxmind_files/`. State plainly that
  they are never committed (licence).
- run via Docker (`docker compose up`) and via the systemd unit.
- the dashboard: where it binds, the `auth_token` warning for non-local `listen`.
- that the build/dev workflow uses the toolchain container (link
  [CONTRIBUTING.md](../CONTRIBUTING.md)); no Go on the host, no Makefile.

## 6.6 End-to-end smoke test

A single integration test (`internal/engine` or a top-level `e2e_test.go`,
`//go:build e2e` tag so it is opt-in):

1. start a UDP listener on `127.0.0.1`.
2. construct a one-ASN `geoip.NewForTest` graph whose single prefix is `127.0.0.0/8`.
3. construct the full stack (config → state → geoip → targets → uploader → metering
   → metrics → engine) pointed at that fixture, web disabled.
4. run the engine for a few short cycles with a deficit forced positive.
5. assert: the listener received datagrams; `metrics.fake_bytes_total` advanced;
   `metering` recorded bytes against the test ASN; `state.json` was written.

Run: `gobox sh -c 'go test -race -mod=vendor -tags e2e ./...'`.

## 6.7 `cross` release build

```sh
gobox sh -c 'CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=vendor -trimpath \
  -ldflags "-s -w" -o tavazon-amd64 ./cmd/tavazon'
gobox sh -c 'CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -mod=vendor -trimpath \
  -ldflags "-s -w" -o tavazon-arm64 ./cmd/tavazon'
```

Both must build offline; both are static (no CGO).

---

## Definition of done

- [ ] `docker build` produces a small static image; `docker compose up` runs it with
      `network_mode: host` and no Redis, reading `.mmdb` from the mounted volume.
- [ ] `cross` yields working amd64 + arm64 static binaries, built offline.
- [ ] README explains GeoLite2 download + placement; no `.mmdb` is committed.
- [ ] The e2e smoke test passes: bytes received, `fake_bytes_total` advanced,
      metering recorded the ASN, state persisted.
- [ ] All 18 flaws in the [STANDARDS.md §12](../STANDARDS.md) table signed off.
- [ ] `gofmt`/`vet` clean; full `go test -race` green; build offline.
