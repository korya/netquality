_default:
    just --list

# Run unit and integration tests (extra args go to `go test`; `-short` keeps only unit tests).
test *args:
    go test -count=1 {{ args }} ./...

# Run only the end-to-end tests: every Test* in files tagged `//go:build e2e` (extra args go to `go test`).
test-e2e *args:
    #!/usr/bin/env sh
    set -eu
    for f in $(git ls-files '*_test.go' | xargs grep -l '^//go:build e2e$'); do
        names=$(grep -o '^func Test[A-Za-z0-9_]*' "$f" | sed 's/^func //' | paste -sd '|' -)
        go test -count=1 -tags e2e -run "^($names)\$" {{ args }} "./$(dirname "$f")"
    done

# Run a real measurement against Cloudflare with the nq CLI (extra args are nq flags, e.g. --json).
measure-cloudflare *args:
    go run ./cmd/nq --target cloudflare {{ args }}

# Run a real measurement against Apple with the nq CLI (extra args are nq flags, e.g. --json).
measure-apple *args:
    go run ./cmd/nq --target apple {{ args }}

# Run every package benchmark without running the regular test suite.
bench:
    go test -run '^$' -bench . -benchmem ./...

# Run public library, CLI, and server API throughput benchmarks.
bench-api:
    go test -run '^$' -bench '^Benchmark(RunLibrary|CLIThroughput|Handler)' -benchmem . ./cmd/nq ./server
