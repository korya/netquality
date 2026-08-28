# Test matrix

Every supported feature and use case maps to at least one automated test that
runs offline (in-process `nqserver` over TLS + HTTP/2). `TestMatrix` in the
root package parses this file and fails if a named test does not exist, so a
row cannot rot silently. Rows marked *live* run only with `NQ_LIVE=1` (nightly
CI) because the behaviour is only observable on real networks.

Format: `| feature | scenario | package | test |`. The package column is a
directory relative to the repo root; `.` is the library.

## Discovery

| Feature | Scenario | Package | Test |
|---|---|---|---|
| Config parsing | Apple, Cloudflare, draft, SERVER_SPEC documents | . | TestParseServerConfigFixtures |
| Config parsing | Invalid documents are rejected (version, duplicates, hosts, scheme) | . | TestParseServerConfigInvalid |
| Config parsing | Unknown fields ignored | . | TestParseServerConfigIgnoresUnknown |
| Well-known target | `WellKnown(host)` builds the draft URL | . | TestWellKnown |
| Discovery over HTTPS | Redirects, invalid body, 404, empty target fail | . | TestDiscoveryErrors |
| Discovery over HTTPS | `ConfigTimeout` bounds a slow server; 429 is reported | . | TestConfigTimeoutAndStatus |
| test_endpoint | Dial override honoured (URLs name an unresolvable host) | . | TestTestEndpointHonoured |
| test_endpoint | Ignored under an explicit proxy, with warning | . | TestExplicitProxyDetected |

## Idle latency

| Feature | Scenario | Package | Test |
|---|---|---|---|
| Fresh-connection probes | Per-stage medians reported | . | TestRunLoopback |
| Skipping | `IdleProbes < 0` skips the phase | . | TestRunDirections |
| Cancellation | Context cancelled during idle probes returns partial result | . | TestCancelDuringIdle |
| TLS normalisation | TLS 1.2 handshake counted as 2 RTTs, TLS 1.3 as 1 | . | TestTLS12Normalisation |
| Statistics | min/median/mean/p95/jitter/trimmed mean | . | TestStatsOf |
| Statistics | Percentile and single-sided trimmed mean | . | TestPercentileAndTrimmedMean |
| Statistics | Stage medians only from staged samples | . | TestComputeLatencyStatsStages |

## Load phases

| Feature | Scenario | Package | Test |
|---|---|---|---|
| Download + upload | Both phases sequentially, HTTP/2, flows ≤ MaxFlows, self+foreign probes | . | TestRunLoopback |
| Direction selection | Download-only / upload-only | . | TestRunDirections |
| Stability algorithm | Draft moving-average criterion, confidence levels (table) | . | TestStabilityTracker |
| Stability algorithm | Moving-average window arithmetic | . | TestStabilityTrackerMovingAverage |
| Stability parameters | Defaults and partial overrides | . | TestDefaultStabilityParams |
| Flow ramp | One flow per interval up to MaxFlows (fake clock) | . | TestMaxFlowsWithFakeClock |
| HTTP/1.1 fallback | No self probes, RPM from foreign probes, warning | . | TestHTTP11Fallback |
| Flow error | Failure before any interval aborts the run | . | TestFlowErrorAbortsPhase |
| Flow error | Failure after intervals truncates with `flow_error`, keeps data | . | TestFlowErrorAfterIntervalsKeepsResult |
| RPM | Draft formula incl. TCP-only case | . | TestResponsiveness |
| RPM | `60000 / RTT` | . | TestRPM |
| Events | Phase/interval/probe/flow/warning events with consistent counts | . | TestEventStream |

## Safety limits

| Feature | Scenario | Package | Test |
|---|---|---|---|
| MaxBytes | Download truncated with `bytes_cap`, warning, mean throughput fallback | . | TestBytesCap |
| MaxBytes | Upload truncated with `bytes_cap` | . | TestUploadBytesCap |
| MaxDuration | Truncated with `duration_cap` when never stable | . | TestDurationCap |
| MaxFlows | Never exceeded | . | TestMaxFlowsWithFakeClock |
| Cancellation | Stops within ~250 ms, partial result flagged `cancelled` | . | TestCancellation |
| Options | Defaults applied; negative IdleProbes preserved | . | TestMaxFlowsDefaultAndOptionsDefaults |

## Proxies

| Feature | Scenario | Package | Test |
|---|---|---|---|
| Explicit proxy | CONNECT proxy detected, credentials stripped, HTTP/2 kept | . | TestExplicitProxyDetected |
| Explicit proxy | No proxy / DIRECT / custom RoundTripper yield nil | . | TestExplicitProxyNilWithoutProxy |
| TLS interception | Verified chain without SCTs flagged with issuer | . | TestTLSInterceptionDetected |
| TLS interception | Chain inspection table (SCT ext, TLS-ext SCTs, vendors, insecure) | . | TestInspectChain |
| Insecure mode | `--insecure` never flags; `proxy` omitted from JSON | . | TestInsecureDoesNotFlagProxy |
| Custom RoundTripper | Warns, probes fall back to unstaged samples | . | TestCustomRoundTripperWarns |

## Result shape

| Feature | Scenario | Package | Test |
|---|---|---|---|
| JSON | snake_case keys, absent directions and empty address lists omitted | . | TestResultJSONShape |
| Local addresses | Loopback run reports `local_ips` = 127.0.0.1 | . | TestRunLoopback |
| Local addresses | Recorded under an explicit proxy (interface toward the proxy) | . | TestExplicitProxyDetected |
| Local addresses | Empty with a custom RoundTripper | . | TestCustomRoundTripperWarns |
| Local addresses | Present on cancelled/partial results, as is `resolved_ips` | . | TestCancellation |
| Local addresses | Address set dedupes, keeps zone IDs, is concurrency-safe | . | TestAddrSet |
| Library example | Compiles and documents usage | . | Example |

## CLI (`cmd/nq`)

| Feature | Scenario | Package | Test |
|---|---|---|---|
| `--json` | stdout is a `Result`, stderr quiet | cmd/nq | TestJSONOutput |
| Human output | Table, progress on stderr, `not run` directions | cmd/nq | TestHumanOutput |
| `--events` | JSON lines on stderr | cmd/nq | TestEventsOutput |
| Exit codes | 0 ok, 1 failed, 2 usage | cmd/nq | TestExitCodes |
| Exit codes | Truncated result still exits 0 with warnings shown | cmd/nq | TestTruncatedStillExitsZero |
| Exit codes | Unreachable target exits 1 | cmd/nq | TestCancelledExitCode |
| `-v` | slog output only when verbose | cmd/nq | TestVerboseLogging |
| `--max-bytes` | Size parsing | cmd/nq | TestParseBytes |
| Formatting | Proxy line, cancelled, warnings, truncation | cmd/nq | TestHelpers |
| `version` | Prints build info | cmd/nq | TestExitCodes |

## Reference server (`server`, `cmd/nqserver`)

| Feature | Scenario | Package | Test |
|---|---|---|---|
| Endpoints | Config, small, large, upload, method checks, HTTP/2 | server | TestHandler |
| Endpoints | `--base-url`, `LargeSize`, HEAD, method-not-allowed matrix | server | TestHandlerBaseURLAndMethods |
| TLS | Self-signed cert covers hosts/IPs, h2 ALPN | server | TestSelfSignedCert |
| Binary | Usage/version/no-TLS/bad cert/bad listen exit codes | cmd/nqserver | TestUsageAndVersion |
| Binary | `--self-signed` server passes a full `nq` run | cmd/nqserver | TestServeSelfSignedEndToEnd |
| Binary | `--cert/--key` + `--base-url` | cmd/nqserver | TestServeWithCertFilesAndBaseURL |
| Build info | ldflags rendering | internal/buildinfo | TestBuildInfoString |

## Live (real networks; `NQ_LIVE=1`)

| Feature | Scenario | Package | Test |
|---|---|---|---|
| Public servers | Apple and Cloudflare produce idle/RPM/throughput without proxy flags | . | TestLive |

### Not testable offline

Stability convergence under real bufferbloat, PTC probe throttling on slow
links, Happy Eyeballs, DNS timing, and real TLS-inspection products. These are
exercised only by the live job, with tolerant assertions.
