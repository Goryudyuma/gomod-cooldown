# Contributing to gomod-cooldown

Thank you for contributing. Bug reports, focused fixes, tests, and
documentation improvements are welcome.

## Before opening an issue

Search existing issues first and include the `gomod-cooldown` version, Go
version, operating system, command, relevant environment settings, and a
minimal reproduction where possible. Report suspected vulnerabilities through
the private process in [SECURITY.md](SECURITY.md), not a public issue.

## Development setup

Go 1.25 or later is required. Clone the repository and run the same core checks
used by CI:

```sh
gofmt -w cmd internal
go mod tidy
go test ./...
go vet ./...
go test -race ./...
golangci-lint run
golangci-lint fmt
```

CI also runs vulnerability analysis and builds and smoke-tests the CLI on
Linux, macOS, and Windows. The pinned tool versions and complete test matrix in
`.github/workflows` are authoritative.

Tests must be deterministic and must not depend on the public module proxy or
other external network services. Prefer `httptest.Server`, injected clocks and
clients, and local fake GOPROXY data. If a test copies an upstream file, record
its immutable source revision, checksum, license, notices, and modification
status in `THIRD_PARTY_NOTICES.md`.

## Pull requests

- Keep each pull request focused and explain the user-visible behavior it
  changes.
- Add regression tests for bug fixes and tests for new behavior.
- Update both `README.md` and `README.ja.md` when changing documented behavior.
- Treat documented v1 flags, defaults, output streams, exit codes, signal
  handling, and module path as compatibility commitments.
- Run the checks above and review generated `go.mod` and `go.sum` changes before
  submitting.

The repository's formatting workflow may open or update a separate formatting
pull request for an in-repository branch. Fork pull requests still receive
ordinary read-only CI checks.

## License

Unless explicitly stated otherwise before submission, any contribution
intentionally submitted for inclusion in this project is provided under the
project's [MIT License](LICENSE). You must have the right to submit the work and
must preserve applicable third-party copyright and license notices.
