Release readiness evidence: hermes-canopy-releng-2026-11-01-23-12-01

Target
- Repository: coding-hermes/hermes-canopy
- Canonical remote: git@github.com:coding-hermes/hermes-canopy.git
- Default branch: master
- Candidate SHA: 94f1f0dc03c20e8b1e81b94929cbc0f42aec0756
- Git and GitHub branch API agree on candidate SHA; origin/master is equal to the candidate.
- Existing release tag: v0.2.0, remote peeled commit 65663f2c9551528f6fa2d32bc849da0bc6c1ff15.
- Release range: v0.2.0..94f1f0dc, 150 commits.

Commit census
- feat: 4
- fix: 20
- perf: 0
- other: 126
- breaking footer: none found
- breaking subject marker: none found
- Proposed semver: v0.3.0 (minor), not authorized for tagging.

CI
- Exact candidate run 41235421846: completed success, Build workflow, SHA 94f1f0dc.
- URL: https://github.com/coding-hermes/hermes-canopy/actions/runs/41235421846
- Recent CI history: latest 20 listed runs are predominantly successful; historical failures include 40822877417, 40817525372, 40816914731 and are not on the candidate.
- Tag-triggered release workflow exists at .github/workflows/release.yml.
- Previous release workflow run 39539678215 on v0.2.0 failed at signed manifest generation because RELEASE_SIGNING_KEY was empty.

Repository release contract
- Project-owned tag workflow: .github/workflows/release.yml.
- Cross-build targets: make build-embed-linux-amd64, build-embed-linux-arm64, build-embed-darwin-amd64, build-embed-darwin-arm64, build-embed-windows-amd64.
- Packages: nfpm deb/rpm; checksums; signed latest.json; GitHub Release upload.
- Local Makefile has no release target.
- Changelog has a v0.2.0 section and no Unreleased section.
- Dockerfile exists, but no configured registry/publish contract was found; no Docker publication attempted.

Local candidate checks in detached worktree
- Cross-build: PASS, make build-embed-linux-amd64 VERSION=v0.3.0 BUILD_TIME=1970-01-01T00:00:00Z.
- Binary probe: PASS, ELF executable and --help returned 0.
- Short suite: PASS, go test ./... -short -count=1 -timeout=480s.
- Vet: PASS, go vet ./....
- Release manifest publication validator: PASS, make validate-release-manifest-publication.
- GitReins: DEGRADED PASS only; working-tree scope had no staged Go files and skipped go_build, go_lint, and go_tests. This is not full release guard evidence.

Dogfood and deployed surface
- Committed .coding-hermes/dogfood-log.md remains PROMISING-BUT-ROUGH through the latest recorded run, with open user-facing findings and partial/unverified install cells. It is not a clean release gate.
- DEPLOY-STALE-001 is closed for the prior deployed service state, but this sweep did not independently re-probe the live binary. Treat deployment freshness for the current candidate as UNVERIFIED.

Blockers and decision
- NO-GO / NO-CUT.
- The scheduled releng tick is not cut authorization; no explicit RELEASE-cut authorization was supplied.
- INT-CI-005 remains pending: repository secret RELEASE_SIGNING_KEY is absent, so signed manifest publication is blocked.
- v0.2.0 has a remote tag but no GitHub Release object; release surface is incomplete.
- FTR-06 remains in_progress for signed cross-platform desktop artifacts and owner-controlled credentials.
- No tag or GitHub Release was created. No existing tag was moved.
