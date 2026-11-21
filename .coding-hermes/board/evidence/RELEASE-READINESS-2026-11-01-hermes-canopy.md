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

---

## Sweep 2026-11-21 — tick hermes-canopy-releng-2026-11-21-12-28-55 (first fire)

Delta only. Everything below re-measured 2026-11-21T12:40Z–12:58Z; sections above are the
2026-11-01 record and stand as history.

Candidate and inventory
- Candidate d3113afeae5583d9b3465063259bdbaf31e7f068, which is simultaneously local HEAD,
  origin/master and the gitlab mirror master; `git rev-list --left-right --count
  origin/master...HEAD` = 0 0 on both. The 11-01 candidate e26777ca is superseded.
- 87 files under product paths changed between the last certified candidate (23b910bf; the 11-01
  record names e26777ca — both give 87, they differ only in bookkeeping paths) and HEAD, excluding
  releng/, .coding-hermes/ and reports/, so the 11-01 content evidence cannot be carried forward.
  118 commits separate e26777ca from HEAD.
- Inventory since the last release artifact v0.2.0 (tag object 8a5fbaa8, peels 65663f2c,
  authored 2026-10-20T22:38:18-05:00): 250 non-merge commits, 254 files changed.
  - release-bearing classes: feat 11, fix 29, docs 23, chore 23, test 3, ci 1
  - bookkeeping classes: board 126, releng 6, foreman 6, qa 3, dogfood 2, ops/env/wip 3
  - breaking: 0 subject `!` tokens and 0 `BREAKING CHANGE` footers across the whole range
  - semver derived: **minor** → proposed v0.3.0 (proposal only, still unauthorized)

CI truth on HEAD
- Actions run 44891600918 on d3113afe: completed / success, one job `build (ubuntu-latest, 1.25)`,
  all 27 steps success — Set up job, checkout, setup-go, tidy, build, README docs drift check,
  vet, golangci-lint, short tests, GAP-098 context-selection accuracy, integration tests,
  MCP SDK canary pin, MCP SDK canary, PG-backed federation+transport, PG-backed card API,
  setup-node, frontend install/build/type-check, gitleaks install, gitleaks detect,
  docker build, deploy.
- Release-workflow runs on tags: exactly one, 39539678215 on 65663f2c (2026-10-21T03:38:24Z),
  completed/failure. No tag has been pushed since.

Version hygiene
- Makefile:19 `VERSION ?= $(shell git describe --tags --always --dirty)` — version is derived,
  there is no version file to bump.
- The candidate therefore self-reports `v0.2.0-274-gd3113afe-dirty`. The `dirty` suffix comes
  from the shared checkout's tracked `.gitreins/tasks.yaml` edit, not from a product change
  (`git status --porcelain` shows only that one path).
- CHANGELOG.md's newest section is still `## [v0.2.0] - 2026-10-21`; there is no `Unreleased`
  section and no v0.3.0 preparation. Gate arm 2 does not hold.
- Cumulative across the range the CHANGELOG convention has lapsed: the v0.2.0 section is the
  last promotion, so 250 commits are undocumented in-tree.

Deployed-binary staleness
- /home/kara/bin/canopyd built 2026-11-21T12:43:24Z with commit=d3113afe (version
  v0.2.0-274-gd3113afe-dirty); pid 418864 restarted 12:43:27Z listening on *:8091.
- Live /healthz: status ok, commit d3113afe, build_time 2026-11-21T12:43:24Z, db up,
  schema_version 49, embedded_migrations 49, relay air_gapped/disabled.
- scripts/check-deploy-staleness.sh: CURRENT (watched source 6aa480ad).
- Net: the 11-01 staleness finding is cleared — the deployed surface is candidate-equivalent.
  Gate arm 6 still fails on the `dirty` version suffix, which needs one rebuild from a clean
  checkout, not a code change.

Release tooling
- The repo's release path is workflow-only and sound at the build stage: release.yml runs the
  five `make build-embed-*` targets (all five verified present and resolvable via `make -n`),
  generates checksums, builds deb/rpm with nfpm v2.43.0, generates the signed manifest,
  validates the publication contract, pushes latest.json to gh-pages, uploads Release assets.
- There is no `make release` target, no scripts/release*, no goreleaser config and no
  docs/releases — the tag→workflow→signed-manifest→gh-pages path is undocumented in-tree.

Blockers (unchanged in kind; one arm newly satisfied)
- RELEASE_SIGNING_KEY is still absent: `gh secret list -R coding-hermes/hermes-canopy` returns
  no secrets at all. release.yml refuses to publish unsigned and hard-fails at
  "Generate signed release manifest" — proven by run 39539678215. INT-CI-005 remains pending.
  This is an owner-supplied credential; not worker-executable.
- v0.2.0's publish leg never completed: the remote tag exists, `gh release list` shows only
  v0.1.0, so neither the Release object nor the binary/manifest assets exist for v0.2.0.
  The tag must not be moved or recreated; the gap needs explicit disposition.
- No cut authorization names v0.3.0, and a scheduled readiness sweep is not authorization.
- No candidate-bound GitReins verdict: no verdict under .gitreins/history mentions
  d3113afeae5583d9b3465063259bdbaf31e7f068. CI is the authority for this candidate.

Decision
- NO-GO / NO-CUT. No tag created, no release published, no existing tag touched.
- Gate arms as of this sweep: arm 1 clean-scoped-tree partial (one tracked vcs-state file),
  arm 2 FAIL (no v0.3.0/Unreleased), arm 3 FAIL (no signing key), arm 4 PASS (run 44891600918
  exact head), arm 5 FAIL (v0.2.0 Release object still absent), arm 6 FAIL (dirty suffix).
- Filed RELEASE-CANOPY-2026-11-21 for the one worker-executable gap (CHANGELOG v0.3.0
  promotion + release-path documentation). Signing key and cut authorization stay with the owner.
- Wave budget 2 carried unused: CI is green at the candidate and every remaining blocker is
  either owner-gated or a single bounded repo task belonging to the foreman, so there was no
  attributable red to hand a releng worker.
