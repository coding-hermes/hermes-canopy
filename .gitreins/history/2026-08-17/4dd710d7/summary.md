# Verdict: GAP-035

**Task:** Sync .env.example with README Environment Variables table
**Evaluated:** 2026-08-17T10:08:20.695726
**Result:** ✓ PASS

## Pipeline Stages

- ✓ **tier1**
  -   ✓ guard: Tier 1 Guards: PASS  (test mode: full)
  ✓ secrets — clean
  ✓ go_build — ok
  ✓ go_lint — ok
  ✓ go
- ✓ **tier2**
  - COMPLETE
  ✓ Every env var in README §Environment Variables appears in .env.example with matching default: Verified all 18 env vars in README §Environment Variables (README.md lines 317-336) appear in .env.example with matching defaults. Commit 5a05fd2 added DB_SCHEMA=public, LOG_FORMAT=text, METRICS_ENABLED=false, CORS_ORIGIN=*, JWT_SECRET=dev-secret-change-me, CANOPY_DB_URL (commented/unset matching README '(unset)'), CONTEXT_MAX_ANCESTORS=50, CONTEXT_MAX_REFS=5, CONTEXT_DEFAULT_BUDGET=8000, PLUGIN_MAX_SIZE=1048576. Pre-existing vars (HTTP_ADDR=:8080, DB_HOST=localhost, DB_PORT=5432, DB_USER=canopy, DB_PASSWORD=canopy, DB_NAME=canopy, DB_SSLMODE=disable, LOG_LEVEL=info) all present with matching defaults. Working tree clean (git status empty for both files).
All 18 env vars from the README Environment Variables table are present in .env.example with matching defaults, verified via git show of both files.

## Summary

Judge Result: GAP-035

Stage tier1: PASS
    ✓ guard: Tier 1 Guards: PASS  (test mode: full)
  ✓ secrets — clean
  ✓ go_build — ok
  ✓ go_lint — ok
  ✓ go

Stage tier2: PASS
  COMPLETE
  ✓ Every env var in README §Environment Variables appears in .env.example with matching default: Verified all 18 env vars in README §Environment Variables (README.md lines 317-336) appear in .env.example with matching defaults. Commit 5a05fd2 added DB_SCHEMA=public, LOG_FORMAT=text, METRICS_ENABLED=false, CORS_ORIGIN=*, JWT_SECRET=dev-secret-change-me, CANOPY_DB_URL (commented/unset matching README '(unset)'), CONTEXT_MAX_ANCESTORS=50, CONTEXT_MAX_REFS=5, CONTEXT_DEFAULT_BUDGET=8000, PLUGIN_MAX_SIZE=1048576. Pre-existing vars (HTTP_ADDR=:8080, DB_HOST=localhost, DB_PORT=5432, DB_USER=canopy, DB_PASSWORD=canopy, DB_NAME=canopy, DB_SSLMODE=disable, LOG_LEVEL=info) all present with matching defaults. Working tree clean (git status empty for both files).
All 18 env vars from the README Environment Variables table are present in .env.example with matching defaults, verified via git show of both files.

Overall: PASS ✓
