# Reference server (`nqserver`)

The self-hostable test server and the `server` package behind it.

Prefix: `SRV`.

---

### SRV-1: Endpoints
`/.well-known/nq` serves the config; `/nq/small` returns 200 with one byte;
`/nq/large` streams `--large-size` bytes (default 8 GiB) of incompressible
data; `/nq/upload` discards a POST/PUT body and replies with the byte count.

### SRV-2: Config content
The config advertises the three URLs under the draft names, with the prefix
from `--base-url` or, when unset, `https://` plus the request's Host; it
includes `test_endpoint` only when `--test-endpoint` is set.

### SRV-3: Methods
Small accepts GET and HEAD; large accepts GET; upload accepts POST and PUT.
Anything else is 405.

### SRV-4: Response hygiene
Bodies use `application/octet-stream`, `Cache-Control: no-store`, no content
encoding, and no redirects.

### SRV-5: TLS and HTTP/2
TLS is mandatory: `--self-signed` generates a certificate for localhost,
loopback, and the listen host; otherwise `--cert/--key` are required and their
absence is a usage error. ALPN offers `h2` then `http/1.1`.

### SRV-7: Bearer-token authentication
With a token configured, every endpoint — the config document included —
requires `Authorization: Bearer <token>` (scheme case-insensitive, credential
compared in constant time). Any missing, malformed, wrong, or oversized
credential gets `401` with `WWW-Authenticate: Bearer realm="nq"` and no body
beyond a short message. Without a token the server is anonymous; the binary
refuses anonymous mode with a real certificate unless `--allow-anonymous`
(`--self-signed` implies it).

### SRV-8: Per-client byte budget
Each client IP has a budget of `--client-bytes` per `--client-window`
(default 2 GiB per 10 min). A large download or upload may start only while
the budget is positive and is charged the bytes it actually moved when it
ends, so one request may overshoot by at most its own cap; a refused request
gets `429` with `Retry-After` in seconds. Running requests are never slowed
down — limits gate the start of work, never shape traffic. The config and
small endpoints are exempt. `-1` disables the budget.

### SRV-9: Request and connection caps
One upload request accepts at most `--upload-size` bytes (default 16 GiB) and
then answers normally; the large download is bounded by `--large-size`.
`--max-connections` (default 256) caps simultaneous connections: further
connections wait in the accept queue rather than fail, so a client bounded by
its own `MaxDuration` still completes with fewer flows.

### SRV-6: Lifecycle
`--listen` defaults to `:8443`; the server logs its bound address, shuts down
gracefully on interrupt, and exits 1 if it cannot listen.
