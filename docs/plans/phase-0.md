# Phase 0 — Foundation

> Plan for [ROADMAP.md](../ROADMAP.md) Phase 0. Stand up a buildable, empty skeleton so
> every later phase has somewhere to land.

**Delivers:** repo skeleton, `go.mod` + vendored `maxminddb-golang`, `.gitignore`,
Docker artifacts — a tree that compiles offline in the toolchain container.
**Depends on:** nothing (only docs exist so far).
**Unblocks:** every other phase.

---

## Tasks

### 0.1 `go.mod`

```
module github.com/salehi/tavazon

go 1.22

require github.com/oschwald/maxminddb-golang v1.13.x
```

- Use the **v1** API (`github.com/oschwald/maxminddb-golang`). v2 (`/v2`) needs a
  newer Go and a different `Networks()` API; project.md §6.5 is written against v1.
- The exact version is whatever the bootstrap step (0.2) resolves; `go.sum` pins it.

### 0.2 Bootstrap the dependency + vendor — **one-time online step**

This is the *only* step that touches the network. Run it once, commit the result,
and every build thereafter is offline.

```sh
gobox go get github.com/oschwald/maxminddb-golang@v1
gobox go mod tidy
gobox go mod vendor
```

Commit `go.mod`, `go.sum`, and the whole `vendor/` tree. Confirm `vendor/modules.txt`
lists only `maxminddb-golang` and its transitive closure (RED_LINES X1).

### 0.3 Package skeleton

Create each `internal/<pkg>/` directory with a single `<pkg>.go` containing just a
package clause and a doc comment, so `go build ./...` succeeds with no real code yet:

```
internal/config/config.go      internal/uploader/uploader.go
internal/netstat/netstat.go    internal/metering/metering.go
internal/geoip/geoip.go        internal/engine/engine.go
internal/state/state.go        internal/metrics/metrics.go
internal/schedule/schedule.go  internal/logging/logging.go
internal/targets/targets.go    internal/web/web.go
internal/sysstat/sysstat.go
```

Each file, e.g.:

```go
// Package config loads, validates, and overlays Tavazon configuration.
// See project.md §7.2.
package config
```

### 0.4 `cmd/tavazon/main.go` — stub

Minimal compilable entrypoint — real wiring comes in Phase 5:

```go
package main

import (
	"flag"
	"fmt"

	_ "time/tzdata" // embed the zoneinfo DB (~450 KB) so Asia/Tehran resolves on a scratch image
)

var version = "dev" // overridden at build time via -ldflags

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("tavazon", version)
		return
	}
	fmt.Println("tavazon: not yet implemented")
}
```

### 0.5 `.gitignore`

```
/tavazon
/tavazon-*
*.mmdb
/data/state.json
/data/metering/
*.log
.claude/settings.local.json
```

### 0.6 `data/.gitkeep` and `maxmind_files/.gitkeep`

Empty files so the otherwise-gitignored `data/` and `maxmind_files/` directories
exist in a fresh clone. The operator's GeoLite2 `.mmdb` files go in `maxmind_files/`;
runtime state and metering are written under `data/`.

### 0.7 `Dockerfile`

Copy verbatim from [project.md §14.1](../project.md): multi-stage, `golang:1.22`
build stage (same image as the dev toolchain — one cached image),
`CGO_ENABLED=0 ... -mod=vendor`, `scratch` final stage (Tavazon needs nothing from a
base image, and it avoids a second registry pull).

### 0.8 `docker-compose.yml`

Copy verbatim from [project.md §14.2](../project.md): single `tavazon` service,
`build: .`, `network_mode: host`, `./data` + `./config.json` volumes, no Redis.

### 0.9 Verify

```sh
gobox sh -c 'CGO_ENABLED=0 go build -mod=vendor ./...'
gobox go vet -mod=vendor ./...
docker build -t tavazon:dev .
```

All three must succeed with no network access after step 0.2.

---

## Definition of done

- [ ] `go build -mod=vendor ./...` succeeds offline in the toolchain container.
- [ ] `vendor/` is committed; `modules.txt` shows only the allowlisted dependency.
- [ ] `docker build` produces an image; `./tavazon -version` prints a version.
- [ ] Repo layout matches [project.md §5](../project.md); no `.github/`, no `Makefile`.
- [ ] `gofmt -l .` prints nothing.
