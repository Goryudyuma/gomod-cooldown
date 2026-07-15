# Security policy

## Supported versions

Security fixes are made on the latest stable release in the supported series.
Users of an older release should upgrade to receive a fix.

| Version | Supported |
| --- | --- |
| Latest stable v1.x release | Yes, after v1.0.0 is released |
| Earlier v1.x releases | Upgrade to latest |
| Latest v0.x release | Until v1.0.0 is released |
| Pre-releases | Best effort |
| Earlier v0.x releases | No |

## Reporting a vulnerability

Please report suspected vulnerabilities privately through the repository's
[GitHub security advisory form](https://github.com/Goryudyuma/gomod-cooldown/security/advisories/new).
Do not open a public issue or pull request containing vulnerability details
until the maintainers have coordinated disclosure with you.

Include a clear description, reproduction steps, affected version, and any
proof of concept. Also describe the expected impact and whether the report is
already public or subject to a disclosure deadline.

The maintainers will acknowledge a report within seven calendar days and aim
to provide an initial assessment within 14 calendar days. Remediation and
disclosure timing depend on severity and complexity; status updates will be
provided through the private advisory while work is in progress.

## Scope notes

`gomod-cooldown` is a dependency-update workflow aid, not a security boundary.
Exact or already-pinned module versions remain downloadable, and standard Go
settings such as `GOPRIVATE` and `GONOPROXY` can bypass its temporary proxy.
Behavior that matches these documented limitations is generally not a
vulnerability, but reports showing an undocumented bypass or unsafe network
exposure are welcome.
