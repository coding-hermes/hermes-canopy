# Release path

This page documents the **measured** release path for canopyd as of
2026-11-22, verified against `.github/workflows/release.yml` and the
`Makefile` at HEAD. It is operational documentation for whoever cuts the next
tag (v0.3.0 is prepared in `CHANGELOG.md` but **no tag exists yet**).

## How a release is published

The only release surface is the GitHub Actions workflow
[`.github/workflows/release.yml`](../../.github/workflows/release.yml). There
is no local `make release` target — `make release` fails with
`No rule to make target 'release'`, and there is no `scripts/release*`
helper. Cutting a release means pushing a `v*` tag.

1. **Trigger.** The workflow runs on push of any `v*` tag (plus manual
   `workflow_dispatch` for re-runs). A concurrency group
   (`release-manifest-publication`, `cancel-in-progress: false`) serializes
   publication.

2. **Cross-build.** The workflow calls five platform Makefile targets:

   - `make build-embed-linux-amd64`
   - `make build-embed-linux-arm64`
   - `make build-embed-darwin-amd64`
   - `make build-embed-darwin-arm64`
   - `make build-embed-windows-amd64`

   The bare `build-embed` target in the Makefile is a sixth, separate
   (non-platform) build for the host architecture — the workflow does not use
   it. Each target produces `bin/canopyd_<os>_<arch>[.exe]`, and the workflow
   generates a `.sha256` checksum for each of the five binaries.

3. **Linux installers.** `nfpm` v2.43.0 packages `deploy/nfpm.yaml` into a
   `.deb` (`dist/canopy_${VERSION}_amd64.deb`) and an `.rpm`
   (`dist/canopy-${VERSION}-1.x86_64.rpm`).

4. **Signed manifest — the signing gate.** `cmd/release-tool` builds
   `dist/latest.json` from the artifacts. This step requires the
   `RELEASE_SIGNING_KEY` repository secret: if it is unset the workflow
   aborts with
   `::error::RELEASE_SIGNING_KEY is required; refusing to publish unsigned latest.json`
   and **the release dies before any publication**. This is not theoretical:
   the last tag run (39539678215) failed exactly this way because
   `RELEASE_SIGNING_KEY` is unset in the repository (`gh secret list` is
   empty). Until the secret is added, no tag can complete a release.

5. **Manifest publication.** The signed `latest.json` is published to the
   `gh-pages` branch at `stable/latest.json` (with a `CNAME` for
   `releases.canopy.app`), byte-identical to the generated manifest
   (`cmp --silent` guards against re-encoding), after
   `make validate-release-manifest-publication` checks the publication
   contract offline.

6. **GitHub Release assets.** `softprops/action-gh-release@v2` uploads the
   five binaries, their checksums, the deb/rpm installers and
   `dist/latest.json` as release assets, with generated release notes.

## Current blockers for the next cut

- `RELEASE_SIGNING_KEY` is unset — every tag run will fail at step 4 until
  the secret is added.
- No `v0.3*` tag exists (verified: `git tag -l 'v0.3*'` and
  `git ls-remote --tags origin 'v0.3*'` are both empty).
- macOS/Windows code signing is intentionally deferred (the workflow makes no
  claim that the cross-built binaries are signed).

## Release status

- Candidate for v0.3.0: `d3113afe` (CI green on that SHA, 27/27 steps).
- Inventory: `CHANGELOG.md` § v0.3.0 (range `v0.2.0`..`d3113afe`).
