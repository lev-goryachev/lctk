#!/usr/bin/env sh
set -eu

cd "$(dirname "$0")/.."

unformatted=$(find cmd internal -name '*.go' -type f -exec gofmt -l {} +)
if [ -n "$unformatted" ]; then
  printf 'Run gofmt on:\n%s\n' "$unformatted" >&2
  exit 1
fi

go vet ./...
go test -cover ./...
