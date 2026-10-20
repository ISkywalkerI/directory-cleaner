#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p artifacts
go version
go mod download
go mod verify
unformatted=$(find cmd internal -name '*.go' -print0 | xargs -0 gofmt -l)
if [[ -n "$unformatted" ]]; then
    printf 'Run gofmt on:\n%s\n' "$unformatted" >&2
    exit 1
fi
go vet ./...
go test -count=1 -timeout=3m -json -coverprofile=artifacts/coverage.out ./... | tee artifacts/tests.jsonl
go tool cover -func=artifacts/coverage.out
go test -race -count=1 -timeout=3m -json ./... | tee artifacts/race.jsonl
go build -o artifacts/dircleaner ./cmd/dircleaner
if [[ "${1:-}" == '--cross-compile' ]]; then
    for target_os in windows linux; do
        for arch in amd64 arm64; do
            suffix=''; [[ "$target_os" != windows ]] || suffix='.exe'
            GOOS="$target_os" GOARCH="$arch" CGO_ENABLED=0 go build -o "artifacts/dircleaner-$target_os-$arch$suffix" ./cmd/dircleaner
            printf 'BUILD ONLY: %s/%s (no runtime verification)\n' "$target_os" "$arch"
        done
    done
fi
