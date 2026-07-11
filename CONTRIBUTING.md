# Contributing to Sawt Gateway

## Development Setup

1. Install Go 1.25+, PostgreSQL, and ffmpeg
2. Copy `.env.example` to `.env` and fill in your credentials
3. Run `go run .` to start the server

## Branch Naming

- `fix/` — bug fixes (e.g., `fix/rate-limiter-proxy`)
- `feat/` — new features (e.g., `feat/health-aggregator`)
- `docs/` — documentation changes

## Pull Request Requirements

1. **All CI checks must pass**: `go build`, `go vet`, `golangci-lint`, `go test -race`
2. **Tests required** for logic changes — add or update tests covering your change
3. **No secrets** in code or config files committed to git
4. **Preserve existing comments and docstrings** unless directly related to your change

## Code Style

- Follow idiomatic Go conventions
- Use `context.Context` for all I/O operations
- Wrap errors with `fmt.Errorf("...: %w", err)`
- Run `golangci-lint run` before submitting

## Testing

```bash
# Full suite
go test ./... -race -cover

# Specific package
go test -run TestName ./internal/workflow/
```

## Regenerating sqlc

After modifying `query.sql`:

```bash
sqlc generate
```

Commit the regenerated files in `database/`.
