Release readiness evidence: hermes-canopy-releng-2026-11-01-23-12-01

Target
- Repository: coding-hermes/hermes-canopy
- Canonical remote: git@github.com:coding-hermes/hermes-canopy.git
- Default branch: master
- Candidate SHA: 1c08ac5ea53362b569c14ab290521ea06405220f
- Git ls-remote and GitHub branch API agree on master at this SHA.
- Existing release tag: v0.2.0, remote peeled commit 65663f2c9551528f6fa2d32bc849da0bc6c1ff15.
- Release range: v0.2.0..1c08ac5ea53362b569c14ab290521ea06405220f, 154 commits.

Commit census
- feat: 4
- fix: 20
- perf: 0
- chore: 5
- docs: 15
- test: 1
- refactor: 0
- other: 109
- breaking footer: none found
- breaking subject marker: none found
- Proposed semver: v0.3.0 (minor), not authorized for tagging.

CI
- Exact candidate Build run 41364034611: completed success on SHA 1c08ac5ea53362b569c14ab290521ea06405220f.
- URL: https://github.com/coding-hermes/hermes-canopy/actions/runs/41364034611
- Exact job build (ubuntu-latest, 1.25) completed success. Build, docs-check, vet, golangci-lint, short tests, context accuracy, integration, MCP SDK canary, PG-backed federation/transport and card tests, frontend build/type-check, gitleaks, Docker build, and deploy steps all completed success.
- Prior candidate runs 41363492772 (ae25c917) and 41362959884 (23b910bf) also completed success; they are not substituted for the current candidate.
- Historical failures 40822877417, 40817525372, and 40816914731 are not on the candidate.
- Tag-triggered release workflow exists at .github/workflows/release.yml.
- Previous v0.2.0 release workflow run 39539678215 failed at signed manifest generation because RELEASE_SIGNING_KEY was empty; subsequent release-workflow history still contains no successful run.

Repository release contract
- Project-owned tag workflow: .github/workflows/release.yml.
- Cross-build targets: make build-embed-linux-amd64, build-embed-linux-arm64, build-embed-darwin-amd64, build-embed-darwin-arm64, build-embed-windows-amd64.
- Published surface: cross-built binaries, sha256 files, nfpm deb/rpm packages, signed latest.json, GitHub Release upload, and gh-pages stable manifest publication.
- Makefile has no release target and there is no scripts/release* or GoReleaser config.
- CHANGELOG.md has a v0.2.0 section and no Unreleased section or v0.3.0 preparation.
- Dockerfile exists and CI builds an unpushed local image, but no configured registry/publish contract was found; no Docker publication attempted.

Local, GitReins, and board evidence
- Current candidate is a clean detached board/evidence worktree at 1c08ac5; the canonical checkout remains dirty with unrelated tracked VFS/GitReins state and an untracked deploy-check temporary file, so it is not a release checkout.
- No candidate-bound GitReins full verdict is available. The newest local verdict, 14dd935e, is PASS/COMPLETE for DEPLOY-STALE-001 evaluated against an earlier board commit and is not candidate evidence. Prior working-tree guard evidence was degraded and skipped Go build/lint/tests.
- Board validator: RESULT: OK with inherited warnings; no new validation failure was observed.

Dogfood and deployed surface
- Committed .coding-hermes/dogfood-log.md remains PROMISING-BUT-ROUGH, with open user-facing findings and partial or unverified install cells. It is not a clean release gate.
- Deployed /home/kara/bin/canopyd reports v0.2.0-143-g4ed731a9, built 2026-10-29T21:00:00Z, not the current candidate. The service is inactive in this probe, although /healthz returned schema 49 matching embedded migrations 49. Deploy freshness is not release-candidate aligned.

Blockers and decision
- NO-GO / NO-CUT.
- This scheduled readiness tick is not explicit cut authorization; no RELEASE-cut authorization names v0.3.0.
- INT-CI-005 remains pending: repository secret RELEASE_SIGNING_KEY is absent, so signed manifest and gh-pages publication are blocked.
- v0.2.0 has a remote tag but gh release view reports no GitHub Release object; do not move or recreate the existing tag.
- FTR-06 remains in_progress for signed native cross-platform artifacts and owner-controlled credentials.
- CHANGELOG/version preparation is incomplete, and no candidate-bound full GitReins verdict or published artifact verification is available.
- No tag or GitHub Release was created. No existing tag was moved.
