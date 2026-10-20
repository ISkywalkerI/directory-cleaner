# Repository Guidelines

## Project Structure & Module Organization

- `cmd/dircleaner/main.go` handles CLI flags, signals, and scan scheduling.
- `internal/config/` parses and validates YAML, byte sizes, and per-directory defaults.
- `internal/cleaner/` scans files, checks stability, deletes eligible files, and writes JSONL audit records. Platform access checks live in `platform_windows.go` and `platform_unix.go`.
- Tests sit beside implementation files as `*_test.go`.
- `directory-cleaner.yaml.example` documents configuration; `deploy/` contains NSSM and systemd service setup. There are no separate frontend assets.

## Build, Test, and Development Commands

Use Go 1.21 or later. Run commands from the repository root:

```text
go mod download
go build ./cmd/dircleaner
go test ./...
go test -cover ./...
go vet ./...
go fmt ./...
```

These download dependencies, build the CLI, run tests, report coverage, check common mistakes, and format Go files, respectively. The binary is `dircleaner.exe` on Windows and `dircleaner` elsewhere.

The complete local gates are `pwsh -File scripts/test.ps1 -CrossCompile` on Windows and `bash scripts/test.sh --cross-compile` on Ubuntu. Reports are written to `artifacts/` (ignored by Git). Ubuntu also runs the race detector. Scripts check formatting without changing files.

Copy `directory-cleaner.yaml.example` to `directory-cleaner.yaml` and adjust paths before running:

```text
go run ./cmd/dircleaner -config directory-cleaner.yaml -once -dry-run
```

Omit `-once` for continuous scanning.

## Coding Style & Naming Conventions

Use `gofmt` formatting with tabs. Keep package names lowercase, exported identifiers in PascalCase, and unexported identifiers in mixedCase. Preserve snake_case YAML and JSON keys. Keep platform-specific behavior behind the existing build constraints. Reuse configuration and audit helpers; avoid new dependencies for small utilities.

## Testing Guidelines

Use Go's standard `testing` package with descriptive `TestXxx` functions. Use `t.TempDir()` for filesystem fixtures; never test deletion against real data. Cover configuration validation and cleanup safeguards when changing those paths. Platform-dependent tests may skip when a required capability, such as symlink creation, is unavailable. No numeric coverage threshold is configured.

For substantial changes to behavior, concurrency, dependencies, platform code or deployment, add relevant regression tests and pass the complete local gate on the current host plus all GitHub Actions matrix jobs before deployment. Required check `CI` summarizes Windows Server 2022, Ubuntu 22.04/24.04 (Go 1.25.12), Ubuntu Go 1.21.13 compatibility, and Windows/Linux amd64/arm64 cross builds. Use a PR after bootstrap; `main` requires `CI` before merging.

Changes to deletion, stability or platform access must pass the real-binary CLI tests and cleaner safeguards. Keep every destructive fixture in a temporary directory. Dependency updates must include `go.sum` and pass `go mod verify`. Record platform, Go version, commands, results and capability skips in the handoff/PR. A skipped test is not evidence for the skipped capability; cross compilation is not runtime validation. Do not claim deployment readiness if a required job is failing or unexecuted. NSSM/systemd service acceptance remains a separate target-machine check in `docs/testing.md`.

## Commit & Pull Request Guidelines

Use concise, imperative subjects such as `Fix stability check before deletion`. Keep changes focused. PRs should describe behavior changes, link relevant issues, and list validation commands and results. Include sample YAML or audit records when their formats change.

## Configuration & Deletion Safety

Keep `dry_run: true` as the default. Review audit output before enabling deletion; `-execute` overrides dry-run for every configured directory. Preserve symlink exclusion, age and stability checks, and validation requiring `trigger_bytes > target_bytes >= 0`. Keep audit logs outside cleanup directories.
