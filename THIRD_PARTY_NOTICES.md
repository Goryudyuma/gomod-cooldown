# Third-party notices

This project statically includes the Go standard library and links
`golang.org/x/mod v0.37.0`; the following Go Authors BSD 3-Clause notice applies:

```text
Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

   * Redistributions of source code must retain the above copyright notice,
     this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above copyright notice,
     this list of conditions and the following disclaimer in the documentation
     and/or other materials provided with the distribution.
   * Neither the name of Google LLC nor the names of its contributors may be
     used to endorse or promote products derived from this software without
     specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT OWNER OR CONTRIBUTORS BE LIABLE FOR
ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES
(INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES;
LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON
ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS
SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

## Large-module test fixtures

The files under `internal/cli/testdata/large-modules` are unmodified,
byte-for-byte copies of upstream `go.mod` files. They are used only as test
fixtures. All three upstream projects license these files under the Apache
License 2.0; a copy of that license is provided in [`LICENSE`](LICENSE).

### Prometheus

- Fixture: `internal/cli/testdata/large-modules/prometheus/go.mod`
- Commit: `e4d0e59c87afd0ca06010a03478ad8f89981085b`
- Source: https://github.com/prometheus/prometheus/blob/e4d0e59c87afd0ca06010a03478ad8f89981085b/go.mod
- Upstream license: https://github.com/prometheus/prometheus/blob/e4d0e59c87afd0ca06010a03478ad8f89981085b/LICENSE
- Upstream notice: https://github.com/prometheus/prometheus/blob/e4d0e59c87afd0ca06010a03478ad8f89981085b/NOTICE
- SHA-256: `5a5c328d946db544e782d28c8a2bf9feab2da530a6a09fe9d59a311f11fab14c`
- License: Apache-2.0
- Modification status: unmodified

The attribution from the upstream Prometheus `NOTICE` that applies to this
fixture is:

```text
The Prometheus systems and service monitoring server
Copyright 2012-2015 The Prometheus Authors

This product includes software developed at
SoundCloud Ltd. (https://soundcloud.com/).
```

The upstream notice entries for components not copied into this repository
are not reproduced here.

### Helm

- Fixture: `internal/cli/testdata/large-modules/helm/go.mod`
- Commit: `68977ec0b5a446286668170dd72bdf59695f8cd5`
- Source: https://github.com/helm/helm/blob/68977ec0b5a446286668170dd72bdf59695f8cd5/go.mod
- Upstream license: https://github.com/helm/helm/blob/68977ec0b5a446286668170dd72bdf59695f8cd5/LICENSE
- SHA-256: `0514561bb6ded52510d15bafb5eace5d60064f954b42f811f8e75d25d0f0d805`
- License: Apache-2.0
- Modification status: unmodified

```text
Copyright 2016 The Kubernetes Authors All Rights Reserved
```

### Caddy

- Fixture: `internal/cli/testdata/large-modules/caddy/go.mod`
- Commit: `873fac5fc094fe538d0c477509127bb321d51a32`
- Source: https://github.com/caddyserver/caddy/blob/873fac5fc094fe538d0c477509127bb321d51a32/go.mod
- Upstream license: https://github.com/caddyserver/caddy/blob/873fac5fc094fe538d0c477509127bb321d51a32/LICENSE
- SHA-256: `ad63430a5588ce8cd311f18c9c572c7de0c491e6d00cedbeb5274ab71fe280ac`
- License: Apache-2.0
- Modification status: unmodified
