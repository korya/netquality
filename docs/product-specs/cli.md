# CLI (`nq`)

Human and machine interfaces of the `nq` binary.

Prefix: `CLI`.

---

### CLI-1: Target selection
`--target cloudflare|cf|apple` (default cloudflare), `--config-url URL`, or
`--well-known host[:port]`; the last two take precedence over `--target`.
An unknown target name is a usage error.

### CLI-2: Limits and parameters
`--max-duration`, `--max-bytes` (accepts `250MB`, `1GB`, `100MiB`, plain
bytes), `--max-flows`, `--idle-probes`, `--interval` map onto the library
options; invalid sizes are usage errors.

### CLI-3: Direction flags
`--download-only` and `--upload-only` select one phase; passing both is a
usage error.

### CLI-4: Human output
Without `--json`, stdout shows target host, HTTP version, resolved IPs and
`via` local IPs, a `Proxy` line when detected, idle latency, one line per
direction (Mbps, RPM, loaded median/p95, flows, confidence, truncation), a
cost line, `Cancelled` when partial, and one `Warning` line per warning.
Progress goes to stderr.

### CLI-5: JSON output
`--json` writes exactly the `Result` document to stdout and nothing to stderr
unless `--events` or `-v` is set.

### CLI-6: Events
`--events` streams one JSON event per line to stderr, replacing the human
progress lines.

### CLI-7: Insecure mode
`--insecure` skips certificate verification for self-hosted servers.

### CLI-8: Exit codes
0 when the run completed (even if truncated); 1 when the run failed or was
interrupted; 2 on usage or configuration errors.

### CLI-9: Version
`nq version` prints the version, commit, and build time injected at build
time, or `devel` with VCS info when built without them.

### CLI-11: Authentication
`--auth-token` (or `NQ_AUTH_TOKEN`) sends `Authorization: Bearer <token>` on
every request; a rejected token exits 1 with the `401` in the message.

### CLI-10: Interrupt
Ctrl-C cancels the run; partial results are printed and the exit code is 1.
