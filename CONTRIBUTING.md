# Contributing to Hermes Canopy

Thank you for your interest in contributing! Hermes Canopy is a graph-native collaboration surface for human-agent work.

## Development Setup

```bash
# Backend
go build ./cmd/canopyd
HTTP_ADDR=:8090 ./canopyd

# Frontend
cd frontend && npm install && npm run dev

# Database (integration tests)
docker compose up -d postgres
```

## Commit Rules

- Every commit MUST include `Co-authored-by: Alexis Okuwa <wojonstech@gmail.com>`
- A `.gitmessage` template is configured — `git commit` auto-includes the co-author
- Never commit secrets, tokens, or passwords
- Run `go vet ./...` and `cd frontend && npx tsc --noEmit` before committing

## Pull Request Process

1. Fork the repository
2. Create a feature branch (`git checkout -b feat/my-feature`)
3. Write tests for your changes
4. Ensure all tests pass: `go test ./...` and `cd frontend && npm test`
5. Run the linters: `go vet ./...` and `cd frontend && npx tsc --noEmit`
6. Commit using the co-author template
7. Push and open a PR against `main`

## Architecture

See `AGENTS.md` for architecture overview, core concepts, and terminology.

## Specs

See `specs/` directory for detailed specifications organized by phase.

## Code Style

- Go: Standard formatting (`gofmt`), idiomatic Go patterns
- TypeScript: Prettier + ESLint as configured in the frontend
- React: Functional components with hooks, no class components
- Tests: `t.Run()` for subtests, `testutil` package for integration helpers

## Questions?

Open a GitHub Discussion or Issue.
