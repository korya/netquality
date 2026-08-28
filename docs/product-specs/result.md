# Result and events

The shape of `Result`, its JSON contract (INV-7), and the progress event
stream.

Prefix: `RES`.

---

### RES-1: Top level
`Result` carries `target`, `started_at`, `duration_ns`, optional `idle`,
optional `download` and `upload`, `cancelled`, and `warnings`.

### RES-2: Omission means not run
`idle`, `download`, `upload`, `target.proxy`, `target.resolved_ips`,
`target.local_ips`, and empty `warnings` are omitted from JSON rather than
emitted as zero values.

### RES-3: Target identity
`target` records the config URL, URL host, `test_endpoint` if any, HTTP
version negotiated by load flows, the parsed config, the server IPs the flows
reached (`resolved_ips`), and the local source IPs they used (`local_ips`).
Address lists are deduplicated, without ports, with IPv6 zone IDs kept.

### RES-4: Direction result
Each direction reports direction name, `throughput_bps`, `peak_throughput_bps`,
`mean_throughput_bps`, `bytes`, `duration_ns`, `flows`, `intervals`, stability
booleans, confidence levels, `truncated`, `reason`, `loaded` latency
(foreign/self/combined), `rpm`, `foreign_rpm`, `self_rpm`, `http_version`,
and `flow_errors`.

### RES-5: Durations and units
Durations are nanoseconds with an `_ns` suffix; throughput is bits per second;
RPM is round trips per minute; bytes are bytes.

### RES-6: Warnings
Every cap hit, fallback, proxy finding, flow error, and idle failure appends a
human-readable string to `warnings`, and is logged at warning level.

### RES-7: Events
`RunWithEvents` delivers `phase` events for discover/idle/download/upload/done,
one `interval` event per completed interval with flows, moving-average
throughput, bytes, and RPM once available, one `probe` event per successful
probe (kind idle/foreign/self, latency), one `flow` event per flow added, and
one `warning` event per warning. Every event is timestamped.

### RES-8: Sink contract
The sink is called synchronously from test goroutines; concurrent calls are
possible and the sink must be safe for that.
