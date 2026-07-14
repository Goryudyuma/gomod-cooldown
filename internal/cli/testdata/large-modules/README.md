# Large-module test fixtures

This directory contains byte-for-byte snapshots of `go.mod` files from three
large public Go projects. Tests copy a snapshot into a temporary directory
before making any test-specific changes; the checked-in snapshots remain
unmodified.

| Fixture | Fixed commit | Immutable source | Immutable license | SHA-256 | License | Modified |
| --- | --- | --- | --- | --- | --- | --- |
| `prometheus/go.mod` | `e4d0e59c87afd0ca06010a03478ad8f89981085b` | [source](https://github.com/prometheus/prometheus/blob/e4d0e59c87afd0ca06010a03478ad8f89981085b/go.mod) | [license](https://github.com/prometheus/prometheus/blob/e4d0e59c87afd0ca06010a03478ad8f89981085b/LICENSE) | `5a5c328d946db544e782d28c8a2bf9feab2da530a6a09fe9d59a311f11fab14c` | Apache-2.0 | No; byte-for-byte copy |
| `helm/go.mod` | `68977ec0b5a446286668170dd72bdf59695f8cd5` | [source](https://github.com/helm/helm/blob/68977ec0b5a446286668170dd72bdf59695f8cd5/go.mod) | [license](https://github.com/helm/helm/blob/68977ec0b5a446286668170dd72bdf59695f8cd5/LICENSE) | `0514561bb6ded52510d15bafb5eace5d60064f954b42f811f8e75d25d0f0d805` | Apache-2.0 | No; byte-for-byte copy |
| `caddy/go.mod` | `873fac5fc094fe538d0c477509127bb321d51a32` | [source](https://github.com/caddyserver/caddy/blob/873fac5fc094fe538d0c477509127bb321d51a32/go.mod) | [license](https://github.com/caddyserver/caddy/blob/873fac5fc094fe538d0c477509127bb321d51a32/LICENSE) | `ad63430a5588ce8cd311f18c9c572c7de0c491e6d00cedbeb5274ab71fe280ac` | Apache-2.0 | No; byte-for-byte copy |

Do not edit these snapshots in place. When updating one, select a new full
commit SHA, replace the file with the exact upstream bytes, recompute its
SHA-256 digest, and update both this file and the repository-level
[`THIRD_PARTY_NOTICES.md`](../../../../THIRD_PARTY_NOTICES.md).

The applicable Prometheus `NOTICE` attribution and Helm copyright statement
are reproduced in `THIRD_PARTY_NOTICES.md`.
