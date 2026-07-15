# gomod-cooldown

[日本語版](README.ja.md)

`gomod-cooldown` keeps newly available Go module versions out of dependency
updates for a configurable cooldown period. It starts a temporary
loopback-only GOPROXY, filters version discovery during the command, then shuts
the proxy down.

> This tool filters version discovery during dependency updates. It does not
> block downloading explicitly requested or already-pinned versions.

It is a workflow aid, not a security boundary or a download-blocking proxy.
It is not affiliated with, endorsed by, or sponsored by the Go project or Google.

## Installation

Go 1.25 or later is required to build or install `gomod-cooldown`.

After v1.0.0 has been published, install that specific stable release for a
reproducible setup:

```sh
go install github.com/Goryudyuma/gomod-cooldown/cmd/gomod-cooldown@v1.0.0
```

After `v1.0.0-rc.1` has been published during the release-candidate period,
use that exact candidate tag instead; the unpublished `@v1.0.0` tag will not
work yet:

```sh
go install github.com/Goryudyuma/gomod-cooldown/cmd/gomod-cooldown@v1.0.0-rc.1
```

Use `@latest` only when intentionally following the newest published release.
It considers a pre-release only when no tagged stable release is available.
Exact versions also make installation independent of version-discovery timing.

Alternatively, build the checked-out source with
`go build ./cmd/gomod-cooldown`.

## Usage

```sh
cd /path/to/your/module
gomod-cooldown --cooldown=14d -- go get -u -t ./...
go mod tidy
```

Run it from the root of the module to update. `-t` includes dependencies of
tests; omit it and use `go get -u ./...` if test dependencies should not be
updated. Review the `go.mod` and `go.sum` diff and run the module's tests after
the update.

Avoid `go get -u all` for this workflow. `all` can retain dependency-internal
packages from the existing package graph while their providing modules are
being upgraded. If a newer module removed one of those internal packages, the
upgrade can fail with "does not contain package" even though the main module
does not import that package directly. Scoping the update to `./...` starts
from packages in the main module instead.

The syntax is `gomod-cooldown [flags] -- command [args...]`; the command is
started directly with its original argv, never through a shell. The child gets
only the temporary local URL as `GOPROXY`; the caller's environment and `go env`
configuration are unchanged.

Flags:

- `--cooldown=14d` accepts Go durations plus `d` as exactly 24 hours (for
  example `168h`, `1.5d`, `7d`, or `14d12h`; `1.5d` is 36 hours). It must be
  positive.
- `--upstream=https://proxy.golang.org` selects the single fixed upstream.
- `--time-source=commit` (default) uses only `.info.Time` and has the same
  per-module discovery shape as normal Go commands. `combined` is an explicit,
  high-cost mode that also uses index timestamps.
- `--upstream-timeout=30s` bounds upstream HTTP requests.
- `--verbose` emits upstream-request and decision diagnostics.

`--help` and `-h` print command help to stdout and exit successfully without
requiring `--`. `--version` prints `gomod-cooldown <version>` to stdout; a
build without embedded module-version metadata reports `devel`.

Exit statuses are part of the v1 CLI contract:

- `0`: the child command succeeded, or help/version was requested.
- `1`: wrapper setup or internal execution failed.
- `2`: CLI usage was invalid.
- `126`: the child command was found but could not be started.
- `127`: the child command was not found.
- Otherwise, a normally exiting child's status is returned unchanged. On
  Linux and macOS, the child and its descendants run in a dedicated process
  group. SIGINT and SIGTERM delivered to the wrapper are forwarded once to
  that group. With an interactive controlling terminal, the child group
  temporarily owns the foreground, so terminal input and terminal-generated
  SIGINT and SIGTERM retain their normal behavior. Signal termination is
  reported as `128 + signal` (`130` for SIGINT and `143` for SIGTERM).

Help and version output use stdout. Wrapper diagnostics use stderr, and the
child process inherits the caller's stdin, stdout, and stderr.

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

If an upstream `@v/list` names a version whose `.info` endpoint returns 404 or
410, the wrapper treats only that unavailable version as unusable and omits it
from discovery. That negative result is cached for the rest of the CLI
invocation. Other `.info` failures, including 403, 429, 5xx, transport errors,
and malformed or inconsistent metadata, continue to fail discovery with 502.

Implicit discovery also avoids selecting a higher `+incompatible` version only
because the cooldown removed the highest usable compatible version named by the
raw list. If the removed compatible version has a real, module-aware `.mod`
file, higher `+incompatible` candidates are omitted. A synthetic legacy `.mod`
containing only `module <path>` is not treated as evidence of module awareness, so those
candidates remain visible. An exact or already-pinned `+incompatible` version
remains downloadable through the version-specific pass-through endpoints.

GOPROXY discovery requests do not tell the proxy the original version query or
the currently selected version. Consequently, this safeguard can also make a
version-prefix query such as `@v2` invisible to implicit discovery; request an
exact version when that distinction matters.

The wrapper intentionally does not append `https://proxy.golang.org,direct` (or
any fallback) to the child's `GOPROXY`: a discovery 404 must not bypass the
cooldown via a later proxy.

Validated `.info` metadata used for version decisions is also cached in memory
and reused only for the lifetime of one CLI invocation. All caches are discarded
at exit and are not carried into the next invocation. The mutable `@v/list` and
`@latest` responses themselves are fetched from the upstream for every request.

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

- Traditional interactive shell job control with `Ctrl-Z` and `fg` is not
  currently supported while the Linux or macOS wrapper has handed the terminal
  foreground to the child. Avoid suspending a wrapped command; SIGINT and
  SIGTERM remain supported. Other Unix targets are best effort: non-terminal
  children use a dedicated process group, while character-device stdin keeps
  the wrapper and child in a shared group to preserve interactive input. The
  one-delivery process-group guarantee above is limited to Linux and macOS.
- Cooldown decisions are made independently for each module path. The tool
  does not solve compatibility across a family of related modules that must be
  released together. If an ecosystem requires coordinated versions, follow
  its upgrade guide or request a known-compatible set of exact versions.
- The tool cannot resolve an ambiguous import caused by the same package being
  present in both a parent module and a newly split nested module. Remove the
  obsolete module requirement or select the intended module versions before
  retrying.
- An exact request such as `go get example.com/mod@v1.2.3`, and a version
  already pinned in `go.mod`, uses version-specific proxy endpoints and is not
  held back by the cooldown. This is intentional and provides an explicit
  escape hatch.
- `GOPRIVATE` and `GONOPROXY` may cause the Go command to bypass this proxy.
  That is normal Go behavior; this tool does not control private-module access.
- Exactly one upstream GOPROXY is supported. The child receives only the local
  proxy URL; no `,direct` or secondary-proxy fallback is added.
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

The minimum supported toolchain is Go 1.25. CI runs the core test suite on Go
1.25.x and the current stable Go release, and tests, builds, and smoke-tests
the CLI on Linux, macOS, and Windows. See
[CONTRIBUTING.md](CONTRIBUTING.md) for the full local verification and
contribution process.

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
End-to-end cases run the real Go command against a local fake GOPROXY. They also
exercise byte-for-byte, fixed-commit `go.mod` snapshots from Prometheus, Helm,
and Caddy; fixture provenance is recorded in
[`internal/cli/testdata/large-modules`](internal/cli/testdata/large-modules).
GitHub Actions runs tests, race detection, vet, `govulncheck`, pinned
`golangci-lint`, and cross-platform build smoke tests. For an in-repository
pull request, a separate workflow runs `gofmt`/`goimports` and opens or updates
a formatting pull request when safe changes are available.

## Compatibility

The v1 compatibility contract covers flag names and meanings, defaults,
stdout/stderr behavior, documented exit codes, the platform-specific signal
behavior documented above, and the `github.com/Goryudyuma/gomod-cooldown`
install/module path. A corrective patch that brings filtering behavior back
into line with the documented policy may change an individual version
decision; such changes are documented in [CHANGELOG.md](CHANGELOG.md).

## License and notices

This project is licensed under the [MIT License](LICENSE). Bundled dependencies
and test fixtures remain under their respective licenses; see
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for details.

## Inspiration

This project was inspired by [imjasonh/go-cooldown](https://github.com/imjasonh/go-cooldown).
It differs by filtering only version discovery endpoints, while allowing
explicitly requested and already-pinned versions to be downloaded.
