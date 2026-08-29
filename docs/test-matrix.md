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
| Statistics | min/median/mean/max/jitter; no percentile at 4 samples | internal/engine | TestStatsOf |
| Statistics | Percentile presence thresholds 5/10/20/100 and values; never equal to the max; highest-present helper | internal/engine | TestPercentilePresenceThresholds |
| Statistics | Real run: 5 idle probes → p80 only, 20 → p95 not p99; loaded sets at the default probe rate carry p80–p99, monotonic | . | TestPercentilesEndToEnd |
| Statistics | Percentile and single-sided trimmed mean | internal/engine | TestPercentileAndTrimmedMean |
| Statistics | Stage medians only from staged samples | internal/engine | TestComputeLatencyStatsStages |

## Load phases

| Feature | Scenario | Package | Test |
|---|---|---|---|
| Download + upload | Both phases sequentially, HTTP/2, flows ≤ MaxFlows, self+foreign probes | . | TestRunLoopback |
| Direction selection | Download-only / upload-only | . | TestRunDirections |
| Stability algorithm | Draft moving-average criterion, confidence levels (table) | internal/engine | TestStabilityTracker |
| Stability algorithm | Moving-average window arithmetic | internal/engine | TestStabilityTrackerMovingAverage |
| Stability parameters | Defaults and partial overrides | internal/engine | TestDefaultStabilityParams |
| Stability algorithm | Windowed series (responsiveness) judged on its values, not on averages of averages | internal/engine | TestWindowedTrackerJudgesValuesOnce |
| Flow ramp | One flow per interval up to MaxFlows (fake clock) | . | TestMaxFlowsWithFakeClock |
| HTTP/1.1 fallback | No self probes, RPM from foreign probes, warning | . | TestHTTP11Fallback |
| Flow error | Failure before any interval aborts the run | . | TestFlowErrorAbortsPhase |
| Flow error | Failure after intervals truncates with `flow_error`, keeps data | . | TestFlowErrorAfterIntervalsKeepsResult |
| Flow error | Upload rejected mid-stream (413): download intact, upload flagged, error carries the status, no stall | . | TestUploadRejectedMidStream |
| Flow error | Redirect on a test URL fails the flow with its status | . | TestRedirectOnTestURLIsAFlowError |
| Stalled server | Headers without body: ends at MaxDuration with duration_cap, zero throughput, probes still running | . | TestStalledLargeBody |
| Byte accounting | Probe cost counts against the byte cap but never as goodput | . | TestProbeCostIsNotGoodput |
| Dead host | Unresolvable test host fails fast with the host in the error | . | TestUnresolvableTestHostFailsFast |
| HTTP/1.1 fallback | Upload direction: no self probes, foreign RPM, warning | . | TestHTTP11FallbackUpload |
| Full ramp | Reaches exactly MaxFlows (16) and never exceeds it | . | TestFullRampToMaxFlows |
| IPv6 | Loopback over `[::1]`: resolved/local IPs, host, full phase (skipped without IPv6) | . | TestIPv6Loopback |
| RPM | Draft formula incl. TCP-only case | internal/engine | TestResponsiveness |
| RPM | `60000 / RTT` | internal/engine | TestRPM |
| Events | Phase/interval/probe/flow/warning events with consistent counts | . | TestEventStream |
| Engine | Per-interval decisions reproduce the pre-extraction loop on a recorded Cloudflare series and synthetic links | internal/engine | TestEngineMatchesReferenceLoop |
| Engine | Probe spacing: 1/MPS floor, PTC stretch on slow links | internal/engine | TestEngineProbeGap |
| Engine | Summary with no completed interval; InitialFlows capped by MaxFlows | internal/engine | TestEngineSummaryFallbacks |
| Engine | Ramp doubles flows until a step gains < `RampGainTolerance`; negative tolerance ramps to the cap; `FlowIncrement` floors a step | internal/engine | TestRampDoublesUntilNoGain |
| Engine | Drain interval: send-buffer credit of new flows excluded from goodput, peak and decisions; immaterial credit costs nothing | internal/engine | TestDrainIntervalExcludesSendBufferCredit |
| Engine | Lower bound = minimum of the latest sustained window of measured intervals, present in a converged flat run; a drain interval breaks the window; none from an unstable series | internal/engine | TestLowerBoundFromSustainedWindow |
| Engine | Goodput drop beyond `ChangeTolerance` restarts tracking and the bound window | internal/engine | TestCapacityDropRestartsGoodputTracking |
| Engine | Phase stops only with both series stable | internal/engine | TestStopNeedsBothSeriesStable |
| Engine | Working-conditions window: since stability when stable (keeps a sparse series' early samples), trailing MAD otherwise, restarted by a goodput drop; phase sample counts | internal/engine | TestWorkingConditionsWindow |
| Lower bound | Both directions report `throughput_lower_bound_bps`, its window and `rpm_upper_bound`; bound ≤ peak; interval events carry `hold` | . | TestLowerBoundInResult |
| Lower bound | A run cut short of four intervals omits the bound | . | TestNoLowerBoundWhenCutShort |
| Loaded window | `loaded_window` spans stability to the end; a series absent from the window but present in the phase is named in a warning (LOAD-14) | . | TestLoadedWindowAndSparseSeriesWarning |
| CLI output | Lower bound and RPM upper bound printed after the estimate, only when a window exists | cmd/nq | TestPrintDirShowsBound |

## Safety limits

| Feature | Scenario | Package | Test |
|---|---|---|---|
| MaxBytes | Download truncated with `bytes_cap`, warning, mean throughput fallback | . | TestBytesCap |
| MaxBytes | Upload truncated with `bytes_cap` | . | TestUploadBytesCap |
| MaxBytes | Counter: limit 0 never trips, positive limit trips exactly once | . | TestByteCounterLimits |
| MaxBytes | Default run on loopback is time-bound only, never `bytes_cap` | . | TestNoByteCapByDefault |
| MaxDuration | Truncated with `duration_cap` when never stable | . | TestDurationCap |
| MaxFlows | Never exceeded | . | TestMaxFlowsWithFakeClock |
| Cancellation | Stops within ~250 ms, partial result flagged `cancelled` | . | TestCancellation |
| Cancellation | Upload phase: body stops on ctx within ~250 ms, partial result | . | TestUploadCancellation |
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
| JSON | snake_case keys, `schema_version` first, absent directions and empty address lists omitted | . | TestResultJSONShape |
| JSON | Marshal/unmarshal round-trip preserves a real Result | . | TestResultJSONRoundTrip |
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
| Binary | `--idle-timeout` closes a quiet HTTP/1.1 keep-alive and HTTP/2 connection; a busy one is untouched (SRV-11) | cmd/nqserver | TestIdleTimeoutClosesQuietConnections |
| Binary | `--cert/--key` + `--base-url`; anonymous request 401, token 200 | cmd/nqserver | TestServeWithCertFilesAndBaseURL |
| Binary | Token from `NQSERVER_AUTH_TOKEN`; `--allow-anonymous` with a real cert | cmd/nqserver | TestTokenFromEnvAndAnonymousOptIn |
| Auth | Bearer parsing: case, whitespace, wrong/prefix/suffix, wrong scheme, oversized, unicode | server | TestAuthorize |
| Auth | Every route returns 401 + WWW-Authenticate without the token | server | TestHandlerAuthOnEveryRoute |
| Limits | Per-client budget: allow while positive, charge actual bytes, refill, per-IP | server | TestClientBudget |
| Limits | 429 + Retry-After when exhausted; config and small exempt; upload cap | server | TestHandlerBudgetAndUploadCap |
| Limits | Connection cap blocks the N+1th accept and releases on close | server | TestLimitListener |
| Connection cap | Closing the listener at the cap releases the waiting Accept (SRV-9) | server | TestLimitListenerCloseAtCap |
| Auth (client) | `Options.Header` reaches config, small, large and upload; overrides User-Agent | . | TestHeadersReachEveryRoute |
| Auth (client) | Full run with token; wrong/missing/Basic/oversized/empty fail at discovery with 401 and zero load bytes | . | TestTokenProtectedServer |
| Limits (client) | Budget exhausted mid-run ends with a flagged flow_error, not a hang | . | TestBudgetExhaustedMidRunIsGraceful |
| Limits (client) | Run completes under a server connection cap | . | TestConnectionCapDoesNotBreakRun |
| `--auth-token` | Flag and `NQ_AUTH_TOKEN`; 401 exits 1 | cmd/nq | TestAuthTokenFlagAndEnv |
| Edge flags | Duration shorter than interval; invalid sizes/durations are usage errors; zero flows/probes mean defaults | cmd/nq | TestEdgeFlags |
| Signed URLs | Fixed test vector; empty key, no path, oversized subject rejected | server | TestSignURLVector |
| Signed URLs | Verify table: order, extra params, rotation, leeway, expiry, max TTL, wrong key, tampered path/sub/exp, malformed, padded/standard base64, percent-encoded path, duplicate params, exp zero/negative/overflow/now, reserved characters in `sub` | server | TestVerifySignature |
| Signed URLs | Anonymous server ignores signatures | server | TestHandlerAnonymousServerAcceptsSignedURLs |
| Signed URLs | Key parsing: hex, base64url, file (hex or raw), too short, missing file | server | TestParseSigningKey |
| Signed URLs | Token or signature suffices; invalid token does not veto a valid signature; config never on signature; budget keyed by subject; expired 401 | server | TestHandlerSignedURLs |
| Signed URLs | Signed-only server: config unreachable, signed small OK | server | TestHandlerSignedOnlyServer |
| Signed URLs (client) | Backend-served signed config runs both directions with no client credential | . | TestSignedURLsEndToEnd |
| Signed URLs (client) | Expired or wrong-key URLs fail with 401 | . | TestSignedURLsRejected |
| Binary | `sign --new-key`, `sign` output, usage errors; `--signing-key` server accepts signed, refuses unsigned; bad env key | cmd/nqserver | TestSignSubcommandAndSigningKeyFlag |
| Build info | ldflags rendering | internal/buildinfo | TestBuildInfoString |

## Live (real networks; `NQ_LIVE=1`)

| Feature | Scenario | Package | Test |
|---|---|---|---|
| Public servers | Apple and Cloudflare produce idle/RPM/throughput without proxy flags | . | TestLive |

## Regression guards

Tests that protect invariants and contracts against future changes rather
than describe a feature. Goldens are regenerated deliberately with
`UPDATE_GOLDEN=1 go test <pkg>`; the diff is the review.

| Guard | Scenario | Package | Test |
|---|---|---|---|
| INV-4 | Eight mixed runs (download, upload, cancelled) leave no goroutine and no open client socket behind, checked the instant `Run` returns | . | TestNoLeaksAcrossRuns |
| Wire contract | Identity encoding on every request, octet-stream POST uploads, GET elsewhere, fresh connection per idle/foreign probe, self probes on load connections | . | TestWireContract |
| No global state | Differently configured runs in one process do not influence each other | . | TestRepeatedRunsAreIndependent |
| INV-7 | Every JSON path and kind of `Result` pinned in `testdata/result_schema.txt`; snake_case enforced | . | TestResultSchemaGolden |
| INV-7 | A stored schema-1 document parses with values intact; `schema_version` readable first; unknown fields ignored | . | TestStoredResultDocumentParses |
| Parsers | Fuzz targets with seed corpora: config document, bearer parsing, signature verification, size flag (CI explores for a few seconds; seeds always run) | ., server, cmd/nq | FuzzParseServerConfig, FuzzAuthorize, FuzzVerifySignature, FuzzParseBytes |
| Cost | Bytes and seconds per algorithm scenario pinned in `internal/engine/testdata/cost_ledger.json` (±15 %); a cheaper run must be recorded, a dearer one fails | internal/engine | TestAlgorithmScenarios |

## Algorithm scenarios (`internal/engine`, `internal/linksim`)

The engine runs against a fluid link model instead of sockets. Scenarios are
judged by oracles: honesty (never `high` confidence when >10 % off), budget
(bytes/time never exceeded), convergence and accuracy. Scenarios the current
algorithm fails are listed in `knownFailing` with a cause; the test fails if
an unlisted scenario regresses **or if a listed one starts passing**, so the
ledger stays exact.

| Feature | Scenario | Package | Test |
|---|---|---|---|
| Algorithm | 5 Mbps–10 Gbps × RTT 10/50/150 ms, bufferbloat, CDN per-flow cap, shaper burst, upload send buffer (20 M, 1 G), tick jitter, mid-run capacity change, metered byte caps (honest truncation); lower bound never above the capacity of its window | internal/engine | TestAlgorithmScenarios |
| Simulator | Delivers exactly capacity, never more | internal/linksim | TestModelDeliversCapacity |
| Simulator | Queue adds queue/capacity of latency | internal/linksim | TestModelQueueAddsLatency |
| Simulator | Token bucket bursts then sustains capacity | internal/linksim | TestModelShaperSustainsCapacity |
| Simulator | Send-buffer credit per flow | internal/linksim | TestModelSendBufferCredit |
| Simulator | Capacity change, byte/duration budgets, determinism | internal/linksim | TestModelCapacityChangeAndBudgets |

### Not testable offline

Stability convergence under real bufferbloat, PTC probe throttling on slow
links, Happy Eyeballs, DNS timing, and real TLS-inspection products. These are
exercised only by the live job, with tolerant assertions.
