# Release checklist

The release workflow intentionally publishes only a Git tag and GitHub's
automatically generated source archives. It does not publish prebuilt binaries.
Tags are annotated but unsigned; the tag ruleset and immutable-release setting
provide the mutation protection without storing a signing key in Actions.

## One-time repository settings

- Apply or verify the checked-in
  [`release` environment settings](environments/release.json). They restrict
  deployments to protected branches and require approval from `Goryudyuma`:

  ```sh
  gh api --method PUT \
    repos/Goryudyuma/gomod-cooldown/environments/release \
    --input .github/environments/release.json
  ```

  This personal repository currently allows the workflow initiator to approve
  their own deployment. Keep `prevent_self_review` disabled until a second
  maintainer can approve releases; enabling it now would deadlock the workflow.
  For the same reason, do not add a `CODEOWNERS` file that names only the pull
  request author while code-owner review is required on `main`.
- Keep the `test`, `lint`, and `format` checks required on `main`.
- Keep immutable releases enabled.
- Apply or verify the checked-in
  [release tag ruleset](rulesets/release-tags.json). It prevents deletion and
  non-fast-forward updates without blocking creation by the Release workflow.
  For a repository without that ruleset, an administrator can create it with:

  ```sh
  gh api --method POST \
    repos/Goryudyuma/gomod-cooldown/rulesets \
    --input .github/rulesets/release-tags.json
  ```

## Before dispatching Release

- Merge every release change to `main` and wait for the CI push run to succeed.
- Start with `v1.0.0-rc.1`; do not publish `v1.0.0` before the release candidate
  has been installed and exercised in representative projects.
- Cut [CHANGELOG.md](../CHANGELOG.md) for the exact tag before dispatching:
  move the relevant entries out of `[Unreleased]` into a
  `## [1.0.0-rc.1] - YYYY-MM-DD` or `## [1.0.0] - YYYY-MM-DD` section, leave an
  empty `[Unreleased]` section, and update the comparison links at the bottom.
  Omit the leading `v` from the section and link labels, but use the exact
  requested `v` tag in the comparison URL.
- For the release candidate, keep the exact RC install command as the runnable
  README example. Before the stable release, update the transitional README
  wording so `@v1.0.0` is presented as already published.
- Copy the full 40-character SHA shown for the successful `main` CI run.
- Confirm `LICENSE`, `THIRD_PARTY_NOTICES.md`, and
  `LICENSES/Apache-2.0.txt` are tracked and correct.
- Dispatch `.github/workflows/release.yml` from `main` with the intended tag and
  exact SHA.

For a new tag, the workflow refuses a non-v1 tag, a non-current `main` SHA, or a
commit without a successful CI push run. It re-checks `main` immediately before
creating the annotated tag. After tag creation, it verifies installation,
module-zip notices, `go version -m`, and `--version` up to six times, waiting 20
seconds and using fresh build, module, and binary caches for every attempt.
It then publishes a source-only GitHub release.

If a transient failure happens after tag creation but before the GitHub Release
is published, re-dispatch with the same tag and exact SHA. The workflow resumes
only when the existing tag is annotated, peels to that SHA, no GitHub Release
exists, and the exact commit still has a successful `main` CI push run. It
skips tag creation and repeats all preflight and tagged-install checks. A
mismatched or lightweight tag, or an existing GitHub Release, always fails
closed.

Never move or delete a release tag. If a repeatable validation failure requires
a code change, fix it on `main` and use the next release candidate or patch
version instead of resuming the old tag.

## Release-candidate verification

From a clean environment, run:

```sh
go install github.com/Goryudyuma/gomod-cooldown/cmd/gomod-cooldown@v1.0.0-rc.1
go version -m "$(go env GOPATH)/bin/gomod-cooldown"
gomod-cooldown --help
gomod-cooldown --version
gomod-cooldown -- go version
```

Exercise the release candidate against the documented representative module
fixtures before publishing `v1.0.0`.
