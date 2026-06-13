# Renaming the Go module path

## The problem

In the source you see this line in [internal/engine/engine.go](../internal/engine/engine.go#L16)
and in every other package:

```go
import "github.com/salehi/tavazon/internal/config"
```

At first glance `github.com/salehi/tavazon` looks like a third-party
dependency that could just be deleted. **It is not.** It is *this project's
own module path* — the name the project gives itself.

It is declared once, in [go.mod](../go.mod):

```
module github.com/salehi/tavazon
```

Every internal package (`internal/config`, `internal/engine`,
`internal/netstat`, …) is addressed by prefixing this module path. So an
import like `github.com/salehi/tavazon/internal/config` simply means
"the `config` package inside *this* repository."

The only genuine external dependencies are the ones under `require` in
`go.mod`:

- `github.com/oschwald/maxminddb-golang`
- `golang.org/x/sys` (indirect)

### Why it cannot just be removed

If you delete the `github.com/salehi/tavazon` line, nothing resolves:

- `go.mod` has no module name → the build fails immediately.
- Every `import "github.com/salehi/tavazon/internal/..."` points at a
  package that no longer has a path → compile errors everywhere.

The string is not noise. It is the spine that every internal import hangs
off of. It can be **renamed**, but not **removed**.

## The fix: rename the module

Renaming is a global, mechanical change. Two parts:

1. Change the `module` line in `go.mod`.
2. Replace the same prefix in every `import` statement across the repo.

### Chosen new name

The module is being renamed to a bare local path:

```
tavazon
```

(A bare name like this works fine for a local/internal project. If the
project ever needs to be `go get`-able as a remote dependency, use a
fetchable path instead, e.g. `github.com/salehi/namizun-go`.)

### Step 1 — edit go.mod

```diff
-module github.com/salehi/tavazon
+module tavazon
```

### Step 2 — rewrite every import

Find every file that references the old prefix:

```sh
grep -rl "github.com/salehi/tavazon" --include="*.go" .
```

At the time of writing that is 13 files (engine, schedule, uploader,
targets, metering, logging, and their `_test.go` files). Replace the
prefix in all of them:

```sh
sed -i 's#github.com/salehi/tavazon#tavazon#g' $(grep -rl "github.com/salehi/tavazon" --include="*.go" .)
```

### Step 3 — verify

```sh
go build ./...
go vet ./...
go test ./...
```

If all three pass, the rename is complete and the code is not corrupted.

## What is NOT part of this rename

`docker-compose.yml` contains:

```yaml
image: salehi/tavazon:latest
```

That `salehi/tavazon` is a **Docker image name/tag**, a completely separate
namespace from the Go module path. It does not affect compilation. Rename
it only if you also want the published container image to be called
something else — that is a deployment decision, not a code-correctness one.

## Summary

| Question | Answer |
|----------|--------|
| Is `github.com/salehi/tavazon` a removable dependency? | No. |
| What is it? | This project's own Go module path (declared in `go.mod`). |
| Can it be deleted? | No — the build breaks without a module path. |
| Can it be changed? | Yes — rename it in `go.mod` and every import. |
| New name | `tavazon` |
| Does the Docker image name change too? | No, unless you choose to — it is unrelated. |
