# netquality [![CI](https://github.com/korya/netquality/actions/workflows/ci.yml/badge.svg)](https://github.com/korya/netquality/actions/workflows/ci.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/korya/netquality.svg)](https://pkg.go.dev/github.com/korya/netquality)

Measure the **capacity, latency and responsiveness** of a network path by
running the IETF *Responsiveness under Working Conditions* test
([draft-ietf-ippm-responsiveness-09][draft]) against Apple's, Cloudflare's, or
your own server. Ships as three things: a **Go library**, the **`nq`** client,
and **`nqserver`**, a reference server you can host yourself.

- **Windows, macOS and Linux** on amd64 and arm64, supported as measurement
  targets rather than just build targets: where a platform's defaults would
  distort a number, the library works around them.
- Standard library only, no CGO, so it adds nothing to your build. Requires
  Go 1.26+ (older lines no longer receive TLS/HTTP security fixes).
- Every run is bounded by **time and connection count** before it starts —
  and by bytes too when you say the link is metered — and every number in
  the result says how it was obtained.
- Sends nothing over the network except the test itself.

```
$ nq --target apple
Target     mensura.cdn-apple.com (HTTP/2.0) 17.253.24.71
Idle       102.2ms median, 131.8ms p80, jitter 32.5ms (5 probes)
Download      178.3 Mbps    365 RPM  loaded 185.2ms median, 512.6ms p99  [8 flows, high/high confidence]
Upload        125.9 Mbps    311 RPM  (>= 121.4 Mbps, <= 330 RPM over 4s)  loaded 160.7ms median, 471.0ms p99  [8 flows, medium/low confidence, TRUNCATED: duration_cap]
Cost       370.5 MB moved in 21.0s
```

## What it measures

1. **Discovery** – fetches the server's JSON config (`/.well-known/nq` or a
   vendor path) to learn the small-download, large-download and upload URLs.
2. **Idle latency** – `IdleProbes` sequential GETs of the 1-byte resource,
   each on a **fresh connection**, so a sample includes DNS + TCP + TLS + HTTP.
   Per-stage medians are reported via `net/http/httptrace`.
3. **Download under load**, then **upload under load** (sequentially, so the
   two loaded-latency figures are distinct). Each phase:
   - opens one HTTP/2 connection to the large resource and adds one more every
     interval (1 s) up to `MaxFlows`;
   - meanwhile fires *foreign* probes (fresh connections) and *self* probes
     (multiplexed on a random load connection), interleaved, at up to
     `MaxProbesPerSecond` and never more than 5 % of measured capacity;
   - declares throughput stable when the standard deviation of the last four
     moving averages is under 5 % of the current one, then does the same for
     responsiveness and stops.
4. **Responsiveness (RPM)** – "round trips per minute", `60000 / RTT_ms`.
   Following the draft:
   `foreign = 60000 / mean(TM(tcp), TM(tls per RTT), TM(http))`,
   `self = 60000 / TM(http_on_loaded_connection)`, `RPM = (foreign+self)/2`,
   where `TM` is the single-sided trimmed mean at the 95th percentile over the
   last four intervals. Roughly: < 300 RPM poor, > 1000 good, > 6000 excellent.
5. **Jitter** – mean absolute deviation of the samples from their mean,
   reported for idle and loaded sets. Percentiles (`p80`/`p90`/`p95`/`p99`)
   appear only when there are enough samples for them to differ from the
   maximum (5/10/20/100 under nearest rank), so the default five idle probes
   yield `p80`, never a fake `p95`.
6. **Cost** – bytes moved and wall time per phase.

## Library

```go
import "github.com/korya/netquality"

res, err := netquality.Run(ctx, netquality.Cloudflare, netquality.Options{
    MaxDuration: 10 * time.Second, // per direction; the budget
    MaxBytes:    100 << 20,        // optional: only on metered links
    MaxFlows:    8,
})
if err != nil { /* discovery failed, or ctx cancelled (res is then partial) */ }
fmt.Println(res.Download.RPM, res.Download.ThroughputBPS, res.Download.Truncated)
```

`Result.Target` records the server IPs the flows reached (`resolved_ips`) and
the local source addresses they went out on (`local_ips`), so a stored result
can be tied to the interface or network it was measured on. Both are present
on cancelled partial results too, and both stay in the result if you share it.

Targets: `netquality.Apple`, `netquality.Cloudflare`, `netquality.WellKnown("host:port")`,
or `netquality.Target{ConfigURL: "..."}`.

`RunWithEvents` takes a `func(Event)` sink for progress bars. Proxies and TLS
settings come through `Options.HTTPClient` (its `*http.Transport` is cloned per
flow so each flow owns a connection). `Options.Logger` accepts a `*slog.Logger`.

`Result` marshals to JSON with stable snake_case names; the CLI's `--json`
output is exactly that struct. `schema_version` (currently 1) is its first
field: it changes only when a field is renamed, removed, retyped, or changes
meaning — never for additions — so stored documents stay interpretable. Directions that did not run are omitted, not
zero. Each `DirectionResult` carries `truncated`, `reason`
(`bytes_cap` | `duration_cap` | `cancelled` | `flow_error`) and the draft's
`throughput_confidence` / `responsiveness_confidence` (`low` | `medium` | `high`).
Whenever four consecutive intervals agreed, a direction also carries
`throughput_lower_bound_bps` — a conservative figure that holds even when the
estimate did not converge — with its `lower_bound_window` and `rpm_upper_bound`
(LOAD-13).

## CLI

```
nq                                   # Cloudflare, both directions
nq --target apple
nq --well-known nq.example.com:8443 [--insecure]
nq --config-url https://host/path/config
nq --download-only | --upload-only
nq --max-duration 8s --max-flows 8          # time is the budget
nq --max-bytes 100MB                         # metered link: add a byte cap
nq --json                            # Result as one JSON document on stdout
nq --events                          # JSON-lines progress on stderr
nq version
```

Exit codes: `0` ok, `1` the test failed or was cancelled, `2` bad usage.

Build with version info:

```
go build -ldflags "-X github.com/korya/netquality/internal/buildinfo.Version=v0.1.0 \
  -X github.com/korya/netquality/internal/buildinfo.Commit=$(git rev-parse HEAD)" ./cmd/nq
```

## Self-hosting `nqserver`

```
go run ./cmd/nqserver --self-signed --listen :8443
nq --well-known localhost:8443 --insecure
```

For real deployments pass `--cert/--key`; `--base-url` sets the advertised URL
prefix when behind a load balancer, `--test-endpoint` advertises a specific
host. The server needs HTTP/2 end to end and must not compress or redirect.

**Access control.** With a real certificate the server refuses to start
anonymously; give it a token and give the client the same one:

```
NQSERVER_AUTH_TOKEN=s3cret nqserver --cert c.pem --key k.pem
NQ_AUTH_TOKEN=s3cret nq --well-known nq.example.com
```

(`--auth-token` works on both; the environment keeps the secret out of `ps`.)
Every endpoint, config included, answers `401` without the token. Library
callers set `Options.Header`. `--allow-anonymous` opts out explicitly;
`--self-signed` implies it for local development.

**Load limits** protect egress without biasing measurements — they gate
whether a request may *start*, never slow one down:

| Flag | Default | Effect |
|---|---|---|
| `--client-bytes` / `--client-window` | 8 GiB / 10 min (unlimited with `--self-signed`) | per client IP; a refused request gets `429` + `Retry-After` |
| `--upload-size` | 16 GiB | bytes accepted by one upload |
| `--large-size` | 8 GiB | bytes served by one download |
| `--max-connections` | 256 | extra connections wait in the accept queue |

Behind a load balancer the client key is the balancer's address; the server
deliberately does not trust `X-Forwarded-For` — use signed URLs with a
subject (below) to key budgets per device instead.

**Signed URLs — no secret on clients.** Your backend serves the config
document with test URLs it has signed; `nqserver` verifies them and the
laptop never holds a reusable credential:

```
nqserver sign --new-key                       # once: a 32-byte key, keep it on the backend and the server
NQSERVER_SIGNING_KEY=<key> nqserver --cert c.pem --key k.pem
nqserver sign --key <key> --ttl 10m --sub laptop-7 https://nq.example.com/nq/small \
    https://nq.example.com/nq/large https://nq.example.com/nq/upload
```

Put the three signed URLs in a config document served by your backend and
point the client at it (`Target{ConfigURL: "https://backend/nq-config"}`).
The client needs no flags. The signature covers only the path, `exp` and
`sub` — `sig = base64url(HMAC-SHA256(key, path + "\n" + exp + "\n" + sub))` —
so any language can issue it, parameter order is irrelevant, and unsigned
parameters are deliberately unprotected (never let a server trust them).
Validity is `exp` + 30 s leeway, at most 24 h — keep issuer and server
clocks in sync, a server clock behind the issuer refuses everything as
"issued too far ahead". Sign the *decoded* path (`/nq/large`, not
`/nq/%6Carge`), percent‑encode `sub` (a raw `+` decodes to a space and fails
closed), and emit `sig` in any base64 flavour. `sub` keys the per-client
budget, so ten laptops behind one NAT get ten budgets. Repeat
`--signing-key` to rotate. Go backends can call `server.SignURL`.

**mTLS** works today without a flag: wrap `server.Handler` in your own
`http.Server` with `TLSConfig.ClientAuth = tls.RequireAndVerifyClientCert`
and `ClientCAs`, and give the client its certificate via
`Options.HTTPClient.Transport.TLSClientConfig.Certificates`. A `--client-ca`
flag is tracked in issue #10.

## Proxies

Corporate laptops often sit behind proxies, in which case the test measures
the laptop→proxy leg, not the path to the server. The result says so rather
than reporting plausible-but-wrong numbers:

- **Explicit proxy** (`HTTPS_PROXY`, PAC, or `Transport.Proxy` on the client
  you pass in): `target.proxy.explicit=true` with the proxy `url`
  (credentials stripped). HTTP/2 still works through CONNECT tunnels.
  `test_endpoint` cannot be honoured, and a warning says so.
- **TLS interception** (Zscaler, Netskope, and similar): the certificate chain
  verifies against the corporate root, but the leaf carries no Certificate
  Transparency SCTs, which every publicly trusted certificate has had since
  2018. `target.proxy.tls_interception=true` with the `issuer` and a `reason`.
  A self-hosted `nqserver` behind a private CA triggers the same flag, since
  the trust situation is identical; the wording says "proxy or private CA".
  `--insecure` skips verification and therefore never triggers it.

Both cases add a warning. Confidence scores are unaffected: the algorithm
converged on a real measurement, just of a shorter path. Not detected: proxies
that re-issue publicly trusted certificates (not possible without a CA
compromise) and transparent TCP-level proxies that pass TLS through untouched
(those are not altering the measurement).

## Safety limits

| Limit | Default | Effect |
|---|---|---|
| `MaxDuration` | 12 s per direction | the budget: phase ends; if not yet stable → `truncated`, `reason=duration_cap`. Cost ≤ rate × 12 s |
| `MaxBytes` | **none** (opt-in) | set on metered links; phase ends → `reason=bytes_cap` |
| `MaxFlows` | 16 | never more concurrent load connections |
| `ctx` cancellation | – | all flows stop within ~200 ms; partial result, `cancelled=true` |

There are no retries, no background goroutines after `Run` returns, and no
telemetry.

## Deviations from the draft

| Item | Draft | Here | Why |
|---|---|---|---|
| Interval (ID) | 5 s | **1 s** | 4 intervals must complete before stability can be declared; with the 12 s per-direction budget a 5 s interval could never stabilise. Earlier drafts and shipping tools use 1 s. Configurable via `Stability.Interval`. |
| Time budget | "implementations may" limit | mandatory `MaxDuration`; `MaxBytes` opt-in | Runs on other people's machines and networks; a time bound makes cost proportional to the link instead of unbounded. |
| Byte cap default | (handoff spec: 250 MB) | none | A fixed byte cap starves fast links of the intervals a confident RPM needs (≈ 8 × rate); the caller knows which networks are metered, the library cannot. |
| Flow error | abort the test | abort the **phase**, report `reason=flow_error`, keep other results | Partial data with a flag beats none. |
| Self probes on HTTP/1.1 | use TCP RTT estimate | omitted; RPM from foreign probes only, warning recorded | TCP_INFO is not portable in pure Go. |
| Flow addition | one per interval | **doubling** each interval while a step gains ≥ 10 % goodput, up to `MaxFlows` | Reaches saturation in ≤ 5 intervals instead of 16, so a 10 Gbps or high-RTT link still settles inside the 12 s budget; a slow link stops after one exploratory flow. |
| Responsiveness tracking | after goodput stability | from the end of the ramp; stability judged on the windowed values, not on averages of them | Removes 3–4 s of latency from every run; the phase still ends only with both series stable. |
| Upload byte accounting | – | intervals inflated by the HTTP/2 send window of new flows are excluded | Bytes are counted when the transport takes them; on a 20 Mbps link the 4 MiB credit otherwise reports 53 Mbps with high confidence. |
| Capacity change | – | a > 25 % goodput drop restarts stability tracking | The draft averages across the change. |
| Probe byte accounting | – | foreign 5000 B, self 1000 B (draft's estimates) | Counted against `MaxBytes` and the 5 % capacity rule. |
| Config `version` | must be `1` | `1` or `"1"` accepted | Lenient on the wire, strict on everything else (duplicates, hosts, scheme). |
| Config field names | `*_download_url`, `upload_url` | also accepts Apple/Cloudflare `*_https_*` names, preferring them | Interop with deployed servers. |
| Cloudflare target | `mach` hardcodes `h3.speed.cloudflare.com` URLs | uses `aim.cloudflare.com/responsiveness/api/v1/config`, which returns the same URLs | Keeps discovery uniform. |

Other constants: `IdleProbes=5` (enough for a median, cheap), `ConfigTimeout=10s`,
in-flight probe cap 64 (bounds goroutines on high-RTT links), TLS handshake
normalised to 1 RTT for TLS 1.3 and 2 for TLS 1.2.

## Testing

```
go test ./...            # unit + end-to-end against an in-process nqserver
NQ_LIVE=1 go test -run TestLive -v .   # hits Apple and Cloudflare
```

[`docs/test-matrix.md`](docs/test-matrix.md) maps every feature and use case
to the test that exercises it; `TestMatrix` fails if a row names a missing
test or a test is missing from the matrix. Behaviours only observable on real
networks (stability under bufferbloat, probe throttling on slow links) run in
the nightly `Live` workflow, which never blocks merges.

## Releasing

1. Move the `[Unreleased]` items in `CHANGELOG.md` under a new `## [X.Y.Z] - date` heading.
2. Commit, then `git tag -a vX.Y.Z -m "netquality vX.Y.Z" && git push origin master vX.Y.Z`.

The `Release` workflow runs the tests, builds `nq` and `nqserver` for all six
platforms, and publishes a GitHub release whose notes are that changelog
section. It fails if the section is missing.

## Licence

Apache-2.0. Written from the draft and Apple's MIT-licensed `SERVER_SPEC.md`;
no code from GPL implementations (e.g. `goresponsiveness`) is included.

[draft]: https://datatracker.ietf.org/doc/draft-ietf-ippm-responsiveness/
