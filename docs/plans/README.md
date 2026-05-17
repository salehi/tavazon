# Tavazon — Implementation Plans

> Granular, file-by-file build plans, one per [ROADMAP.md](../ROADMAP.md) phase. Each
> plan lists tasks in dependency order — work them top to bottom. Every Go command
> runs in the toolchain container ([project.md §16](../project.md)); there is no
> `Makefile` and no Go on the host.

## How to use these

1. Do phases in order — each depends on the previous being **done** (its
   definition-of-done checked).
2. Within a phase, do tasks in the numbered order — later tasks import earlier ones.
3. A package lands with its tests in the **same** change (STANDARDS.md §10).
4. After each phase: `gofmt -l .` empty, `go vet` clean, `go test -race` green,
   `go build` offline — all in the toolchain container.
5. If reality forces a change to a signature or design, update
   [project.md](../project.md) in the same change — it stays the source of truth.

## Phases

| Plan | Phase | Delivers |
|------|-------|----------|
| [phase-0.md](phase-0.md) | Foundation | Buildable skeleton, vendored dep, Docker artifacts |
| [phase-1.md](phase-1.md) | Data & I/O | `config`, `netstat`, `geoip`, `state` |
| [phase-2.md](phase-2.md) | Scheduling & targeting | `schedule` (modes + curve), `targets` |
| [phase-3.md](phase-3.md) | Traffic engine & observability | `uploader`, `metering`, `metrics`, `logging` |
| [phase-4.md](phase-4.md) | Orchestration | `engine` |
| [phase-5.md](phase-5.md) | Interface | `web` + dashboard, `cmd/tavazon/main.go` |
| [phase-6.md](phase-6.md) | Deployment & smoke test | Docker, systemd, README, e2e test |

## Toolchain shorthand

All plans assume this prefix (define it as a shell function locally — not committed):

```sh
gobox() { docker run --rm -u "$(id -u):$(id -g)" -e GOCACHE=/tmp/gocache \
  -v "$PWD":/work -w /work golang:1.22 "$@"; }
```

Then: `gobox go test -race -mod=vendor ./...`, etc. See project.md §16.
