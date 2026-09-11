# Run every package benchmark without running the regular test suite.
bench:
    go test -run '^$' -bench . -benchmem ./...

# Run public library, CLI, and server API throughput benchmarks.
bench-api:
    go test -run '^$' -bench '^Benchmark(RunLibrary|CLIThroughput|Handler)' -benchmem . ./cmd/nq ./server
