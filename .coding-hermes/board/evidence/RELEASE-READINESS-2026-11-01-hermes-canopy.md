Release readiness evidence: hermes-canopy-releng-2026-11-01-23-12-01

Target
- Repository: coding-hermes/hermes-canopy
- Canonical remote: git@github.com:coding-hermes/hermes-canopy.git
- Default branch: master
- Candidate SHA: e26777cacb97e040b271dab8d47717b0189e6be1
- git ls-remote, GitHub branch API, and origin/master agree on this SHA.
- Existing release tag: v0.2.0, remote peeled commit 65663f2c9551528f6fa2d32bc849da0bc6c1ff15.
- Release range: v0.2.0..e26777cacb97e040b271dab8d47717b0189e6be1, 157 commits.

Commit census
- feat: 4
- fix: 20
- perf: 0
- chore: 5
- docs: 15
- test: 1
- refactor: 0
- other: 112
- breaking footer: none found
- breaking subject marker: none found
- Proposed semver: v0.3.0 (minor), not authorized for tagging.

CI
- Exact candidate Build run 41366597643: completed success on SHA e26777cacb97e040b271dab8d47717b0189e6be1.
- URL: https://github.com/coding-hermes/hermes-canopy/actions/runs/41366597643
- Exact job build (ubuntu-latest, Go 1.25) completed success. Every listed step completed success, including Build, docs drift, vet, golangci-lint, short tests, context accuracy, integration, MCP SDK canary, PG-backed federation/transport and card tests, frontend build/type-check, gitleaks, Docker build, and deploy.
- Tag-triggered release workflow exists at .github/workflows/release.yml and triggers on v* tags.
- Previous v0.2.0 release workflow run 39539678215 failed at signed manifest generation because RELEASE_SIGNING_KEY was empty; gh secret list currently has no RELEASE_SIGNING_KEY.

Repository release contract
- Project-owned tag workflow: .github/workflows/release.yml.
- Cross-build targets: make build-embed-linux-amd64, make build-embed-linux-arm64, make build-embed-darwin-amd64, make build-embed-darwin-arm64, and make build-embed-windows-amd64.
- Published surface: cross-built binaries, sha256 files, nfpm deb/rpm packages, signed latest.json, GitHub Release upload, and gh-pages stable manifest publication.
- Makefile has no release target and there is no scripts/release* or GoReleaser config.
- make validate-release-manifest-publication passed in the clean candidate worktree.
- CHANGELOG.md has a v0.2.0 section and no Unreleased section or v0.3.0 preparation.
- Dockerfiles exist and CI builds an unpushed local image, but no configured registry/publish contract was found; no Docker publication attempted.

Local and GitReins evidence
- Candidate evidence is bound to clean detached worktree SHA e26777ca. The canonical checkout is dirty with unrelated tracked VFS/GitReins state and an untracked deploy-check temporary file, so it is not a release checkout.
- GIT_CONFIG_GLOBAL=/dev/null go test -short -count=1 ./... passed in the clean candidate worktree; all packages completed successfully.
- make validate-release-manifest-publication passed.
- Candidate-bound gitreins guard completed with rc=0 but DEGRADED PASS: secrets clean; go_build, go_lint, and go_tests skipped because no Go files were in working-tree scope. This is bounded evidence, not a comprehensive full verdict.
- The repository's current .gitreins/verdicts/verdict.json is PASS/COMPLETE for earlier commit 9aaf7c27 and is not candidate evidence. No candidate-bound full GitReins verdict is available.
- Board validator: RESULT: OK with 517 inherited warnings; no new validation failure was observed.

Dogfood and deployed surface
- Committed .coding-hermes/dogfood-log.md remains PROMISING-BUT-ROUGH, with open user-facing findings and partial or unverified install cells. It is not a clean release gate.
- Deployed canopyd reports v0.2.0-143-g4ed731a9, built 2026-10-29T21:00:00Z, not the current candidate. check-deploy-staleness.sh reports CURRENT against its watched source set, but no release-candidate deployment is proven and the binary carries a development-distance version.

Blockers and decision
- NO-GO / NO-CUT.
- This scheduled readiness tick is not explicit cut authorization; no RELEASE-cut authorization names v0.3.0.
- INT-CI-005 remains pending: RELEASE_SIGNING_KEY is absent, so signed manifest and gh-pages publication are blocked.
- v0.2.0 has a remote tag but gh release list shows only v0.1.0; do not move or recreate the existing v0.2.0 tag.
- FTR-06 remains in_progress for signed native cross-platform artifacts and owner-controlled credentials.
- CHANGELOG/version preparation is incomplete, and no candidate-bound full GitReins verdict or published artifact verification is available.
- No tag or GitHub Release was created. No existing tag was moved.
