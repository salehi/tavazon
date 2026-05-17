# Contributing to Tavazon

> How to work on this codebase day to day. Read [RED_LINES.md](RED_LINES.md) (hard
> boundaries) and [STANDARDS.md](STANDARDS.md) (engineering standards) first — this
> document assumes both. The design is [project.md](project.md); the build order is
> [ROADMAP.md](ROADMAP.md).

---

## 1. Prerequisites

- **Docker.** That is the whole toolchain. Go is **not** installed on the host — every
  Go command runs inside a container (§2). There is no `Makefile`.
- A POSIX shell.
- The two **GeoLite2 `.mmdb` files** (see §3) to *run* the daemon — and not even to
  build. The `geoip` test uses them when present and skips otherwise; every other
  test uses the in-memory `geoip.NewForTest`.

The build needs **no network**: the one dependency (`maxminddb-golang`) is vendored in
`vendor/`, and every build uses `-mod=vendor`. The runtime targets Linux (it reads
`/proc/net/dev`); the container runs with `network_mode: host` so it sees the host's
real interfaces and RX/TX counters.

---

## 2. The toolchain container

Every interaction with Go happens inside a throwaway `golang` container with the repo
bind-mounted at `/work` — see [project.md §16](project.md). The pattern:

```sh
docker run --rm -u "$(id -u):$(id -g)" -e GOCACHE=/tmp/gocache \
  -v "$PWD":/work -w /work golang:1.22 <go command>
```

It is worth wrapping that prefix in a shell alias/function for your own convenience
(personal, not committed — the repo intentionally ships no build script):

```sh
gobox() { docker run --rm -u "$(id -u):$(id -g)" -e GOCACHE=/tmp/gocache \
  -v "$PWD":/work -w /work golang:1.22 "$@"; }
```

Then the day-to-day commands are:

```sh
gobox sh -c 'CGO_ENABLED=0 go build -mod=vendor -trimpath -o tavazon ./cmd/tavazon'
gobox go test -race -mod=vendor ./...
gobox go vet -mod=vendor ./...
gobox gofmt -l .                       # lists unformatted files; empty = clean
gobox go mod vendor                    # after any go.mod change
```

- `go test -race` needs a C compiler — keep the Debian-based `golang:1.22` image, not
  `-alpine`.
- `-u "$(id -u):$(id -g)"` keeps generated files (`vendor/`, the binary) owned by you,
  not root.

**Running the daemon** is done via Docker too — `docker compose up` builds the image
from the `Dockerfile` and runs it with `network_mode: host`. For a quick local config
inspection without the engine:

```sh
docker compose run --rm tavazon -print-config
```

Keep `web.listen` on `127.0.0.1` for local work. Binding non-local without an
`auth_token` is a configuration error (project.md §8, security note).

---

## 3. GeoLite2 databases

Tavazon resolves destination IPs from MaxMind GeoLite2 data. The `.mmdb` files are
**never committed** — the MaxMind EULA forbids redistribution (RED_LINES X12).

- Create a free MaxMind account and download **GeoLite2-ASN** and **GeoLite2-Country**
  in `.mmdb` form.
- Place them at `maxmind_files/GeoLite2-ASN.mmdb` and
  `maxmind_files/GeoLite2-Country.mmdb` (or point `geoip.asn_db` / `geoip.country_db`,
  or the `-asn-db` / `-country-db` flags, elsewhere). `docker-compose.yml` mounts
  `maxmind_files/` into the container read-only, so it sees them.
- `.gitignore` already excludes `*.mmdb`. Never `git add -f` one.
- Most tests do **not** need these files — they use the in-memory `geoip.NewForTest`.
  Only `geoip`'s own `Open` test reads the real files, and it `t.Skip`s when absent.

---

## 4. Branching & commits

- Work on a topic branch off the default branch — never commit directly to it.
- Branch names: `phase0-skeleton`, `feat-asn-picker`, `fix-reboot-detection`.
- One logical change per commit. Keep the tree green at every commit — the
  toolchain-container build and `go test -race` must pass.
- Commit messages: a concise imperative subject (≤ 72 chars), then a body explaining
  *why* if it is not obvious. Reference the design when relevant:
  `schedule: make the 24h curve C1-continuous (project.md §6.4)`.
- A `go.mod` change is paired with a regenerated `vendor/` (`gobox go mod vendor`) in
  the same commit.
- Commit or push only when asked; if asked while on the default branch, branch first.

---

## 5. Quality gates (there is no CI)

By design the repo has **no `.github/` workflows** and **no `Makefile`**
(project.md §16). Gates are local and manual — run them in the toolchain container
(§2) before every push:

- `gobox go vet -mod=vendor ./...` — clean, no suppressions.
- `gobox go test -race -mod=vendor ./...` — all green.
- `gobox gofmt -l .` — prints nothing.
- `gobox sh -c 'CGO_ENABLED=0 go build -mod=vendor ./...'` — compiles offline.
- A quick self-check that no non-allowlisted import or stray `.mmdb` slipped in
  (RED_LINES X1, X12).

A change is not ready to push until all of these pass. Release binaries are built with
the `cross` command (project.md §16) — static amd64 + arm64, in the container.

---

## 6. Adding code

- Land work in [ROADMAP.md](ROADMAP.md) phase order — do not build a layer before the
  layer it depends on is done and tested.
- New packages go under `internal/` and match the layout in project.md §5.
- A feature lands with its tests. A bug fix lands with a regression test that fails
  before the fix and passes after (STANDARDS.md §10).
- Adding a dependency is almost always wrong — the allowlist is closed (RED_LINES X1).
  If one is genuinely unavoidable, project.md and RED_LINES.md change first, in review.
- If behavior diverges from project.md, update project.md in the same change — it
  stays the source of truth.
- If a need conflicts with a red line, the design doc changes first, in review. There
  is no exception process for [RED_LINES.md](RED_LINES.md).

---

## 7. Pull requests

A PR description states **what** changed, **why**, and which ROADMAP phase / project.md
section it advances. Before requesting review, self-check the
[STANDARDS.md §13 review checklist](STANDARDS.md):

- [ ] In the toolchain container: `gofmt -l .` empty; `go vet` clean; `go test -race`
      green; `go build` offline (`-mod=vendor`).
- [ ] No new third-party import; allowlist unchanged; no committed `.mmdb` at all.
- [ ] Crosses no line in RED_LINES.md.
- [ ] Every error checked; every new goroutine has panic recovery.
- [ ] No new periodic/modal/constant pattern in generated traffic (STANDARDS.md §9).
- [ ] New behavior tested; bug fixes have a regression test.
- [ ] Exported symbols documented; project.md updated if the design moved.
- [ ] The flaws table (STANDARDS.md §12) only moves toward *resolved*.

Reviewers check the PR against RED_LINES.md and the STANDARDS.md checklist explicitly.

---

## 8. Reporting issues

- **Bugs:** include the effective config (`-print-config`, with `auth_token` redacted),
  relevant log lines, and what you expected versus what happened.
- **Security / ethical concerns:** anything that looks like it could let Tavazon
  concentrate traffic or behave like an attack tool is high priority — raise it
  directly and reference the relevant RED_LINES.md item.
- Never paste a real `auth_token`, a server's real IP, or a MaxMind license key into
  an issue.
