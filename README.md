# gomod-cooldown

[日本語版](README.ja.md)

`gomod-cooldown` is a local wrapper for dependency updates. It starts a
temporary loopback-only GOPROXY, filters version discovery during the command,
then shuts the proxy down.

> This tool filters version discovery during dependency updates. It does not
> block downloading explicitly requested or already-pinned versions.

It is a workflow aid, not a security boundary or a download-blocking proxy.
It is not affiliated with, endorsed by, or sponsored by the Go project or Google.

## Installation

```sh
go install github.com/Goryudyuma/gomod-cooldown/cmd/gomod-cooldown@latest
```

Or build the checked-out source with `go build ./cmd/gomod-cooldown`.

## Usage

```sh
gomod-cooldown --cooldown=14d -- go get -u all
```

The syntax is `gomod-cooldown [flags] -- command [args...]`; the command is
started directly with its original argv, never through a shell. The child gets
only the temporary local URL as `GOPROXY`; the caller's environment and `go env`
configuration are unchanged.

Flags:

- `--cooldown=14d` accepts Go durations plus `d` as exactly 24 hours (for
  example `168h`, `7d`, or `14d12h`). It must be positive.
- `--upstream=https://proxy.golang.org` selects the single fixed upstream.
- `--time-source=commit` (default) uses only `.info.Time` and has the same
  per-module discovery shape as normal Go commands. `combined` is an explicit,
  high-cost mode that also uses index timestamps.
- `--upstream-timeout=30s` bounds upstream HTTP requests.
- `--verbose` emits upstream-request and decision diagnostics.

## What is filtered

Only these version-discovery endpoints are filtered:

```
/<module>/@v/list
/<module>/@latest
```

All version-specific endpoints, including `.info`, `.mod`, and `.zip`, and all
other GOPROXY endpoints are passed through to the configured upstream. Therefore
an explicit request such as `go get example.com/mod@v1.2.3` and a version already
recorded in `go.mod` can be downloaded even during its cooldown.

The wrapper intentionally does not append `https://proxy.golang.org,direct` (or
any fallback) to the child's `GOPROXY`: a discovery 404 must not bypass the
cooldown via a later proxy.

## Availability time

`.info.Time` is a commit time, not a publish time: a new tag can point at an old
commit. This is the default availability source because it only requires
per-module proxy requests and remains practical for interactive use.

The official `index.golang.org` feed supplies the time a module version was
first cached by `proxy.golang.org`. That first-cached time is an availability
time, not an exact tag publish time.

`--time-source=combined` is deliberately opt-in. The index has no per-module
lookup API, so startup reads the complete chronological global feed from the
cutoff through the present using `since` and `limit=2000`, paging until the final
short page. It can take a long time for a large cooldown window. A malformed
record, HTTP failure, timeout, or cursor that does not advance fails setup closed;
it never silently falls back to commit time. This mode requires the exact
official `https://proxy.golang.org` upstream.

For each discovery candidate:

```
availableAt = max(commitTime, firstCachedTime)
cutoff = now - cooldown
allow when availableAt <= cutoff
```

Versions absent from the fully read recent snapshot are treated as first-cached
before the cutoff. The index/proxy relationship can race, so this is not a
strict security boundary.

If an upstream `@latest` is too recent, the wrapper filters `@v/list` and
returns the highest eligible tagged release; if no release remains it chooses
the highest tagged pre-release. It does not invent pseudo-versions. A module
whose only usable older history is pseudo-versions may therefore return 404 for
`@latest`, while a pinned pseudo-version remains downloadable via its
version-specific endpoints.

## Notes and troubleshooting

- `GOPRIVATE` and `GONOPROXY` may cause the Go command to bypass this proxy.
  That is normal Go behavior; this tool does not control private-module access.
- The module cache may satisfy a command without a network request. Use a fresh
  cache when testing proxy behavior.
- A 502 means discovery metadata or the complete index snapshot could not be
  validated. The wrapper logs the cause to stderr rather than treating it as an
  empty list.
- Excluded versions are logged with module, version, commit time, available
  first-cached time, effective availability time, and cutoff.
- This is not a substitute for Dependabot security updates. It is intended to
  be used alongside them.

## Development

```sh
gofmt -w cmd internal
go mod tidy
go test ./...
go vet ./...
go test -race ./...
golangci-lint run
golangci-lint fmt
```

Tests use `httptest.Server`, injected clients/clocks, and no external network.
GitHub Actions runs tests, race detection, vet, and `golangci-lint`; a separate
weekly (or manually dispatched) workflow opens a formatting PR when safe
`gofmt`/`goimports` changes are available.

## License and notices

This project is licensed under [Apache License 2.0](LICENSE). See
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for bundled dependency notices.

## Inspiration

This project was inspired by [imjasonh/go-cooldown](https://github.com/imjasonh/go-cooldown).
It differs by filtering only version discovery endpoints, while allowing
explicitly requested and already-pinned versions to be downloaded.
